package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFencing_EqualOrHigherTokenIsAccepted(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	err := store.withWriteTx(ctx, func(tx execer) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_fencing (workspace_id, fencing_token, leader_id, updated_at) VALUES (?, ?, ?, ?)`,
			"w1", 5, "leader-a", nowUTC())
		return err
	})
	require.NoError(t, err)

	err = store.withWriteTx(ctx, func(tx execer) error { return checkFencing(ctx, tx, "w1", 5) })
	require.NoError(t, err)
	err = store.withWriteTx(ctx, func(tx execer) error { return checkFencing(ctx, tx, "w1", 6) })
	require.NoError(t, err)
}

func TestFencing_StaleTokenIsRejected(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	err := store.withWriteTx(ctx, func(tx execer) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO workspace_fencing (workspace_id, fencing_token, leader_id, updated_at) VALUES (?, ?, ?, ?)`,
			"w1", 5, "leader-a", nowUTC())
		return err
	})
	require.NoError(t, err)

	err = store.withWriteTx(ctx, func(tx execer) error { return checkFencing(ctx, tx, "w1", 4) })
	require.Error(t, err)
	var fencingErr *FencingError
	require.ErrorAs(t, err, &fencingErr)
	require.Equal(t, int64(4), fencingErr.Incoming)
	require.Equal(t, int64(5), fencingErr.Current)
}

func TestFencing_StaleLeaderCannotMutateAfterNewLeaderElected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"

	oldToken, err := store.AcquireLease(ctx, ws, "leader-old")
	require.NoError(t, err)
	require.Equal(t, int64(1), oldToken)

	newToken, err := store.AcquireLease(ctx, ws, "leader-new")
	require.NoError(t, err)
	require.Equal(t, int64(2), newToken)

	// The old leader's in-flight write, still carrying its now-superseded
	// token, must be rejected (§6).
	_, err = store.AppendAdd(ctx, ws, oldToken, "r1", KindFollowUp, map[string]any{"a": float64(1)}, StorageInline)
	require.Error(t, err)
	var fencingErr *FencingError
	require.ErrorAs(t, err, &fencingErr)

	// The new leader's write, carrying the current token, succeeds.
	_, err = store.AppendAdd(ctx, ws, newToken, "r1", KindFollowUp, map[string]any{"a": float64(1)}, StorageInline)
	require.NoError(t, err)
}

func TestFencing_AcquireLeaseIsMonotonicPerWorkspace(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	t1, err := store.AcquireLease(ctx, "ws-a", "leader-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), t1)
	t2, err := store.AcquireLease(ctx, "ws-a", "leader-2")
	require.NoError(t, err)
	require.Equal(t, int64(2), t2)

	// A different workspace has its own independent counter.
	tOther, err := store.AcquireLease(ctx, "ws-b", "leader-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), tOther)
}
