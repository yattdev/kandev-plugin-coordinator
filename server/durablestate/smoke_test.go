package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open("file:" + t.Name() + "?mode=memory&cache=shared")
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSmoke_AddUpdateCompactSnapshotReplay(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "workspace-1"

	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)
	require.Equal(t, int64(1), token)

	_, err = store.AppendAdd(ctx, ws, token, "fu-1", KindFollowUp, map[string]any{"title": "check the thing"}, StorageInline)
	require.NoError(t, err)

	updateMutation, err := store.AppendUpdate(ctx, ws, token, "fu-1", map[string]any{"title": "check the thing", "status": "resolved"}, StorageInline)
	require.NoError(t, err)

	rec, found, err := store.GetRecord(ctx, ws, "fu-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "resolved", rec.Body["status"])

	snap, err := store.CaptureSnapshot(ctx, ws, TriggerPreRollup, token)
	require.NoError(t, err)
	require.Equal(t, 1, snap.RecordCount)

	receipt, err := store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "fu-1", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "committed", receipt.Phase)
	require.Len(t, receipt.RolledRecords, 1)

	_, found, err = store.GetRecord(ctx, ws, "fu-1")
	require.NoError(t, err)
	require.False(t, found)

	// Replaying up to the update's mutation_id (before the rollup's remove)
	// reconstructs fu-1's resolved-but-not-yet-archived body.
	beforeRemove, err := store.Replay(ctx, ws, receipt.PreState.SnapshotID, ReplayOptions{TargetMutationID: &updateMutation.MutationID})
	require.NoError(t, err)
	require.Contains(t, beforeRemove, "fu-1")
	require.Equal(t, "resolved", beforeRemove["fu-1"]["status"])

	// Full replay with no cutoff applies the rollup's remove too, matching
	// the actual current-state surface (fu-1 gone).
	final, err := store.Replay(ctx, ws, receipt.PreState.SnapshotID, ReplayOptions{})
	require.NoError(t, err)
	require.NotContains(t, final, "fu-1")
}
