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

	pruned, err := store.PruneMutations(ctx, ws, token, []string{snap.SnapshotID})
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
	token := setupWorkspaceWithRecords(t, store, ws, 2)

	pruned, err := store.PruneMutations(ctx, ws, token, nil)
	require.NoError(t, err)
	require.Empty(t, pruned)

	remaining, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
}

func TestRetention_PruneMutationsRejectsStaleFencingToken(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3) // mutation_ids 1,2,3

	snap, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
	require.NoError(t, err) // watermark 3

	newToken, err := store.AcquireLease(ctx, ws, "leader-b")
	require.NoError(t, err)
	require.Greater(t, newToken, token)

	_, err = store.PruneMutations(ctx, ws, token, []string{snap.SnapshotID})
	require.Error(t, err, "a fenced-out leader must not be able to prune append-only mutation history")
	var fencingErr *FencingError
	require.ErrorAs(t, err, &fencingErr)

	remaining, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	require.Len(t, remaining, 3, "no mutation-log rows may be removed when the fencing check itself fails")
}

// TestRetention_PruneMutationsProtectsRowsNeededByUncommittedCompaction
// reproduces the scenario PruneMutations' doc comment describes: a rollup
// crashes between §5 steps (c) and (d), leaving its receipt stuck in phase
// "archived". If a snapshot is then captured (advancing the retention
// watermark past the rollup's own remove-mutation entries) and retention
// prunes mutation-log rows purely by watermark, it would delete the exact
// remove-mutation rows verifyRolledRecordMutationLinkage needs to ever
// safely resume and commit that compaction -- permanently stranding it.
// PruneMutations must instead leave those specific rows in place until the
// receipt reaches phase "committed", and ResumeCompactions must still
// succeed afterward.
func TestRetention_PruneMutationsProtectsRowsNeededByUncommittedCompaction(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3) // mutation_ids 1,2,3 -> ra, rb, rc

	// archiveCompaction (steps a-c) only: leaves the receipt in phase
	// "archived", simulating a crash before step (d)'s current-state swap.
	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-stuck", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)
	require.Len(t, receipt.RolledRecords, 1)
	protectedMutationID := receipt.RolledRecords[0].MutationID
	require.NotZero(t, protectedMutationID)

	// Advance the retention watermark past the protected mutation_id.
	snap, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
	require.NoError(t, err)

	prunable, err := store.PrunableMutationIDs(ctx, ws, []string{snap.SnapshotID})
	require.NoError(t, err)
	require.Contains(t, prunable, protectedMutationID, "watermark-only candidate selection must still name the protected row (the protection must come from PruneMutations itself)")

	pruned, err := store.PruneMutations(ctx, ws, token, []string{snap.SnapshotID})
	require.NoError(t, err)
	require.NotContains(t, pruned, protectedMutationID, "must not prune a mutation-log row still needed by an uncommitted (archived-phase) compaction receipt")

	remaining, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	found := false
	for _, m := range remaining {
		if m.MutationID == protectedMutationID {
			found = true
		}
	}
	require.True(t, found, "the protected remove-mutation row must still exist in the mutation log")

	// The stuck compaction must still be resumable/committable afterward.
	resumed, err := store.ResumeCompactions(ctx, ws, token)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	require.Equal(t, "committed", resumed[0].Phase)
}
