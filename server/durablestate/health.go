package durablestate

import (
	"context"
	"database/sql"
	"time"
)

// bumpHealthCounter increments a narrow, named health counter for
// workspaceID. This is observability only — it never participates in
// fencing or the mutation lane, so it does not take the write lock any
// mutating operation depends on beyond its own row.
func (s *Store) bumpHealthCounter(ctx context.Context, workspaceID, counterName string, delta int64) {
	// Best-effort: a health counter write failing must never fail the
	// operation it is observing.
	_, _ = s.db.ExecContext(ctx,
		`INSERT INTO health_counters (workspace_id, counter_name, value, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (workspace_id, counter_name) DO UPDATE SET value = value + excluded.value, updated_at = excluded.updated_at`,
		workspaceID, counterName, delta, nowUTC())
}

func (s *Store) readHealthCounter(ctx context.Context, workspaceID, counterName string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM health_counters WHERE workspace_id = ? AND counter_name = ?`, workspaceID, counterName,
	).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return v, err
}

// Health surfaces the narrow set of metrics this package's boundaries call
// for (compaction verification, receipt failures, lease/recovery counters
// where stored, and snapshot/archive age/size) — enough for the
// runtime/harness owner to alert/dashboard on without this package
// exposing scheduler policy.
type Health struct {
	// CompactionVerificationFailures counts §4 set-equality/disjointness
	// validation failures observed by archiveCompaction (a rollup that was
	// rejected before any mutation was applied).
	CompactionVerificationFailures int64
	// ReplayFailures counts ReplayError occurrences observed by Replay
	// (hash mismatch, unavailable content_ref, correlation mismatch,
	// duplicate mutation_id, or any other fail-closed abort).
	ReplayFailures int64
	// LeaseAcquisitions counts every AcquireLease call (leader
	// elections/renewals) recorded so far.
	LeaseAcquisitions int64
	// CompactionRecoveries counts every receipt ResumeCompactions has had
	// to finish (i.e. a crash was detected between §5 steps (c) and (d)).
	CompactionRecoveries int64
	// StuckCompactions is the current count of receipts still in phase
	// "archived" (not yet committed) for this workspace right now — a
	// non-zero value here means ResumeCompactions has work to do.
	StuckCompactions int
	// SnapshotCount is the number of currently-retained snapshots.
	SnapshotCount int
	// OldestSnapshotAge / NewestSnapshotAge are computed against wall-clock
	// now from the oldest/newest retained snapshot's timestamp (zero value
	// if there are no snapshots).
	OldestSnapshotAge time.Duration
	NewestSnapshotAge time.Duration
	// ArchiveRecordCount is the total number of rows ever appended to the
	// archive table for this workspace.
	ArchiveRecordCount int
	// OldestArchiveAge / NewestArchiveAge mirror the snapshot ages above,
	// against the archive table's appended_at column.
	OldestArchiveAge time.Duration
	NewestArchiveAge time.Duration
	// CurrentFencingToken is the workspace's current leader fencing token.
	CurrentFencingToken int64
}

// GetHealth computes the current Health surface for workspaceID.
func (s *Store) GetHealth(ctx context.Context, workspaceID string) (*Health, error) {
	h := &Health{}
	var err error
	if h.CompactionVerificationFailures, err = s.readHealthCounter(ctx, workspaceID, "compaction_verification_failures"); err != nil {
		return nil, err
	}
	if h.ReplayFailures, err = s.readHealthCounter(ctx, workspaceID, "replay_failures"); err != nil {
		return nil, err
	}
	if h.LeaseAcquisitions, err = s.readHealthCounter(ctx, workspaceID, "lease_acquisitions"); err != nil {
		return nil, err
	}
	if h.CompactionRecoveries, err = s.readHealthCounter(ctx, workspaceID, "compaction_recoveries"); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM compaction_receipts WHERE workspace_id = ? AND phase = 'archived'`, workspaceID,
	).Scan(&h.StuckCompactions); err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	var snapshotCount int
	var oldestSnap, newestSnap sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(timestamp), MAX(timestamp) FROM snapshots WHERE workspace_id = ?`, workspaceID,
	).Scan(&snapshotCount, &oldestSnap, &newestSnap); err != nil {
		return nil, err
	}
	h.SnapshotCount = snapshotCount
	if oldestSnap.Valid {
		if t, err := time.Parse(time.RFC3339Nano, oldestSnap.String); err == nil {
			h.OldestSnapshotAge = now.Sub(t)
		}
	}
	if newestSnap.Valid {
		if t, err := time.Parse(time.RFC3339Nano, newestSnap.String); err == nil {
			h.NewestSnapshotAge = now.Sub(t)
		}
	}

	var archiveCount int
	var oldestArchive, newestArchive sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(appended_at), MAX(appended_at) FROM archive WHERE workspace_id = ?`, workspaceID,
	).Scan(&archiveCount, &oldestArchive, &newestArchive); err != nil {
		return nil, err
	}
	h.ArchiveRecordCount = archiveCount
	if oldestArchive.Valid {
		if t, err := time.Parse(time.RFC3339Nano, oldestArchive.String); err == nil {
			h.OldestArchiveAge = now.Sub(t)
		}
	}
	if newestArchive.Valid {
		if t, err := time.Parse(time.RFC3339Nano, newestArchive.String); err == nil {
			h.NewestArchiveAge = now.Sub(t)
		}
	}

	if h.CurrentFencingToken, err = s.CurrentFencingToken(ctx, workspaceID); err != nil {
		return nil, err
	}
	return h, nil
}
