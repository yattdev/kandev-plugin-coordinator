package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestore_ReactivateRecordAppendsAddMutationAndRestoresLiveRow(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 2) // ra, rb

	receipt, err := store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "committed", receipt.Phase)

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.False(t, found)

	// Reconstruct ra's archived body via replay (read-only inspection,
	// §7 steps 1-4), then reactivate it as a live obligation again (§7
	// step 5's mutating restore).
	reconstructed, err := store.Replay(ctx, ws, receipt.PreState.SnapshotID, ReplayOptions{})
	require.NoError(t, err)
	_ = reconstructed // ra is not present in the post-rollup replay result

	archived, err := resolveArchiveRef(ctx, store.db, ws, receipt.CompactionID, "ra")
	require.NoError(t, err)

	reactivation, err := store.ReactivateRecord(ctx, ws, token, "", "ra", KindFollowUp, archived, StorageInline)
	require.NoError(t, err)
	require.Equal(t, "committed", reactivation.Phase)
	require.Equal(t, ReceiptRestoreReactivation, reactivation.Kind)

	rec, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, archived, rec.Body)

	// The mutation log carries the reactivation as an add with
	// compaction_id: null and a non-empty restore_id.
	mutations, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	found = false
	for _, m := range mutations {
		if m.RecordID == "ra" && m.Op == OpAdd && m.RestoreID != "" {
			found = true
			require.Empty(t, m.CompactionID)
		}
	}
	require.True(t, found, "expected a restore_reactivation add mutation for ra")
}

func TestRestore_ReactivateRecordIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 1)

	receipt, err := store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	archived, err := resolveArchiveRef(ctx, store.db, ws, receipt.CompactionID, "ra")
	require.NoError(t, err)

	first, err := store.ReactivateRecord(ctx, ws, token, "restore-1", "ra", KindFollowUp, archived, StorageInline)
	require.NoError(t, err)
	require.Equal(t, "committed", first.Phase)

	second, err := store.ReactivateRecord(ctx, ws, token, "restore-1", "ra", KindFollowUp, archived, StorageInline)
	require.NoError(t, err)
	require.Equal(t, first.CompactionID, second.CompactionID)

	mutations, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	restoreMutations := 0
	for _, m := range mutations {
		if m.RestoreID == "restore-1" {
			restoreMutations++
		}
	}
	require.Equal(t, 1, restoreMutations, "retrying ReactivateRecord must never re-append the mutation-log entry")
}

func TestRestore_TornReactivationBetweenLogAppendAndApplyIsResumed(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 1)

	receipt, err := store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	archived, err := resolveArchiveRef(ctx, store.db, ws, receipt.CompactionID, "ra")
	require.NoError(t, err)

	// Simulate a crash after the mutation-log append but before the
	// current-state write (§7 step 5's crash/retry rule).
	stuck, err := store.appendReactivationMutation(ctx, ws, token, "restore-2", "ra", KindFollowUp, archived, StorageInline)
	require.NoError(t, err)
	require.Equal(t, "archived", stuck.Phase)

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.False(t, found, "current-state write must not have happened yet")

	resumed, err := store.ReactivateRecord(ctx, ws, token, "restore-2", "ra", KindFollowUp, archived, StorageInline)
	require.NoError(t, err)
	require.Equal(t, "committed", resumed.Phase)

	_, found, err = store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found)

	mutations, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	restoreMutations := 0
	for _, m := range mutations {
		if m.RestoreID == "restore-2" {
			restoreMutations++
		}
	}
	require.Equal(t, 1, restoreMutations)
}
