package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetention_PruneSnapshotsRemovesOldestBeyondKeepCountAndReceipts(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 1)

	var snapIDs []string
	for i := 0; i < 4; i++ {
		snap, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
		require.NoError(t, err)
		snapIDs = append(snapIDs, snap.SnapshotID)
	}

	receipt, err := store.PruneSnapshots(ctx, ws, token, 2)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	require.Equal(t, ReceiptSnapshotPrune, receipt.Kind)
	require.Equal(t, "committed", receipt.Phase)
	require.Len(t, receipt.RolledRecords, 2)

	remaining, err := store.ListSnapshots(ctx, ws)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
	remainingIDs := map[string]bool{}
	for _, s := range remaining {
		remainingIDs[s.SnapshotID] = true
	}
	require.True(t, remainingIDs[snapIDs[2]])
	require.True(t, remainingIDs[snapIDs[3]])

	receipts, err := store.ListCompactionReceipts(ctx, ws)
	require.NoError(t, err)
	found := false
	for _, r := range receipts {
		if r.Kind == ReceiptSnapshotPrune {
			found = true
		}
	}
	require.True(t, found, "pruning must durably receipt itself")
}

func TestRetention_PruneSnapshotsIsNoOpBelowKeepCount(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 1)
	_, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
	require.NoError(t, err)

	receipt, err := store.PruneSnapshots(ctx, ws, token, 5)
	require.NoError(t, err)
	require.Nil(t, receipt)

	remaining, err := store.ListSnapshots(ctx, ws)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}

func TestRetention_PruneSnapshotsRejectsStaleFencingToken(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 1)
	for i := 0; i < 3; i++ {
		_, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
		require.NoError(t, err)
	}
	newToken, err := store.AcquireLease(ctx, ws, "leader-b")
	require.NoError(t, err)
	require.Greater(t, newToken, token)

	_, err = store.PruneSnapshots(ctx, ws, token, 1)
	require.Error(t, err)
	var fencingErr *FencingError
	require.ErrorAs(t, err, &fencingErr)
}

func TestRetention_PruneMutationsRemovesEntriesAtOrBelowRetainedWatermark(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3) // mutation_ids 1,2,3

	_, err := store.AppendUpdate(ctx, ws, token, "ra", map[string]any{"n": float64(99)}, StorageInline)
	require.NoError(t, err) // mutation_id 4

	snap, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
	require.NoError(t, err) // watermark should be 4

	_, err = store.AppendUpdate(ctx, ws, token, "ra", map[string]any{"n": float64(100)}, StorageInline)
	require.NoError(t, err) // mutation_id 5, after the watermark, must survive

	pruned, err := store.PruneMutations(ctx, ws, []string{snap.SnapshotID})
	require.NoError(t, err)
	require.Len(t, pruned, 4)

	remaining, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, int64(5), remaining[0].MutationID)
}

func TestRetention_PruneMutationsIsNoOpWithNoRetainedSnapshots(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	setupWorkspaceWithRecords(t, store, ws, 2)

	pruned, err := store.PruneMutations(ctx, ws, nil)
	require.NoError(t, err)
	require.Empty(t, pruned)

	remaining, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
}
