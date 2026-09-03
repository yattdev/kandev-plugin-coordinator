package durablestate

import (
	"context"
	"database/sql"
	"fmt"
)

// FencingError reports a stale (superseded) fencing token being rejected
// (§6). Callers can type-assert/errors.As this to distinguish "fenced out"
// from other failures (e.g. to trigger a re-election rather than a retry).
type FencingError struct {
	Incoming int64
	Current  int64
}

func (e *FencingError) Error() string {
	return fmt.Sprintf("durablestate: stale fencing_token %d < current leader fencing token %d; write rejected (superseded leader)", e.Incoming, e.Current)
}

// AcquireLease elects (or renews) leadership for workspaceID, advancing its
// fencing token by exactly one and recording leaderID as the current
// holder. This is the only operation that ever advances the stored
// fencing token — every other mutating operation only compares its caller-
// supplied token against it (checkFencing). Returns the new token.
//
// This package deliberately does not implement lease TTLs, renewal
// scheduling, or leader-election policy (PLUGIN_SCALE_RFC.md §2.1/§2.4) —
// that belongs to the runtime/scheduler owner. AcquireLease only provides
// the durable, monotonic counter primitive that policy is built on.
func (s *Store) AcquireLease(ctx context.Context, workspaceID, leaderID string) (int64, error) {
	var token int64
	err := s.withWriteTx(ctx, func(tx execer) error {
		row := tx.QueryRowContext(ctx,
			`SELECT fencing_token FROM workspace_fencing WHERE workspace_id = ?`, workspaceID)
		var current int64
		switch err := row.Scan(&current); err {
		case nil:
			token = current + 1
			_, err := tx.ExecContext(ctx,
				`UPDATE workspace_fencing SET fencing_token = ?, leader_id = ?, updated_at = ? WHERE workspace_id = ?`,
				token, leaderID, nowUTC(), workspaceID)
			return err
		case sql.ErrNoRows:
			token = 1
			_, err := tx.ExecContext(ctx,
				`INSERT INTO workspace_fencing (workspace_id, fencing_token, leader_id, updated_at) VALUES (?, ?, ?, ?)`,
				workspaceID, token, leaderID, nowUTC())
			return err
		default:
			return err
		}
	})
	if err != nil {
		return 0, fmt.Errorf("durablestate: acquiring lease for workspace %q: %w", workspaceID, err)
	}
	s.bumpHealthCounter(ctx, workspaceID, "lease_acquisitions", 1)
	return token, nil
}

// CurrentFencingToken returns the highest fencing token ever accepted for
// workspaceID (0 if no lease has ever been acquired).
func (s *Store) CurrentFencingToken(ctx context.Context, workspaceID string) (int64, error) {
	var token int64
	err := s.db.QueryRowContext(ctx,
		`SELECT fencing_token FROM workspace_fencing WHERE workspace_id = ?`, workspaceID,
	).Scan(&token)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("durablestate: reading fencing token for workspace %q: %w", workspaceID, err)
	}
	return token, nil
}

// checkFencing mirrors replay_reference.py's check_fencing: a write's
// fencing_token must be >= the stored current token. A late-arriving write
// carrying a lower (superseded) token is rejected. Must be called from
// inside the same write transaction as the mutation it is guarding, so the
// check and the write it protects are atomic.
func checkFencing(ctx context.Context, tx execer, workspaceID string, incoming int64) error {
	row := tx.QueryRowContext(ctx,
		`SELECT fencing_token FROM workspace_fencing WHERE workspace_id = ?`, workspaceID)
	var current int64
	switch err := row.Scan(&current); err {
	case nil:
		// fall through
	case sql.ErrNoRows:
		current = 0
	default:
		return err
	}
	if incoming < current {
		return &FencingError{Incoming: incoming, Current: current}
	}
	return nil
}
