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

func TestRestore_ReplayFromSnapshotPreservesIntegerNumberHashes(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"

	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)

	original := map[string]any{"attempt": int64(1), "score": 1.5}
	_, err = store.AppendAdd(ctx, ws, token, "ra", KindFollowUp, original, StorageInline)
	require.NoError(t, err)

	receipt, err := store.Compact(ctx, ws, token, "compaction-number-parity", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "committed", receipt.Phase)

	replayed, err := store.Replay(ctx, ws, receipt.PreState.SnapshotID, ReplayOptions{})
	require.NoError(t, err)
	require.NotContains(t, replayed, "ra")

	preRemoveMutationID := receipt.RolledRecords[0].MutationID - 1
	beforeRemove, err := store.Replay(ctx, ws, receipt.PreState.SnapshotID, ReplayOptions{TargetMutationID: &preRemoveMutationID})
	require.NoError(t, err)
	require.Equal(t, original, beforeRemove["ra"])
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

// TestRestore_ArchivedPhaseRetryIgnoresDivergentCallerBody is the exact
// adversarial repro for the reported restore_id idempotency defect: a
// retry of ReactivateRecord for a restore_id already stuck in the
// "archived" phase (crashed after the mutation-log append, before the
// current-state apply) must commit the body that was already durably
// appended to the mutation log, never the fresh (and here, deliberately
// different) body argument the caller happens to pass on retry. If the
// retry instead used its own body argument, current_state would diverge
// from what the append-only mutation log says was reactivated.
func TestRestore_ArchivedPhaseRetryIgnoresDivergentCallerBody(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 1)

	receipt, err := store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	original, err := resolveArchiveRef(ctx, store.db, ws, receipt.CompactionID, "ra")
	require.NoError(t, err)

	// First phase only: append the mutation-log entry, but crash before
	// the current-state apply (mirrors the torn-write test above).
	stuck, err := store.appendReactivationMutation(ctx, ws, token, "restore-3", "ra", KindFollowUp, original, StorageInline)
	require.NoError(t, err)
	require.Equal(t, "archived", stuck.Phase)

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.False(t, found, "current-state write must not have happened yet")

	// Retry with a *different* body than what was logged. A correct,
	// idempotent implementation must ignore this divergent body and apply
	// only what the mutation log already recorded.
	divergent := map[string]any{"resolution": "this-was-never-appended-to-the-log"}
	require.NotEqual(t, original, divergent)

	resumed, err := store.ReactivateRecord(ctx, ws, token, "restore-3", "ra", KindFollowUp, divergent, StorageInline)
	require.NoError(t, err)
	require.Equal(t, "committed", resumed.Phase)

	rec, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, original, rec.Body, "archived-phase retry must apply the durably-logged body, not the caller's divergent retry body")

	sha, err := canonicalHash(original)
	require.NoError(t, err)
	require.Equal(t, sha, rec.SHA256)

	mutations, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	restoreMutations := 0
	for _, m := range mutations {
		if m.RestoreID == "restore-3" {
			restoreMutations++
			require.NotNil(t, m.After)
			require.Equal(t, original, m.After.Body, "the mutation log itself must still hold the original body, not the divergent retry body")
		}
	}
	require.Equal(t, 1, restoreMutations, "retrying ReactivateRecord must never re-append the mutation-log entry")
}

// TestRestore_ArchivedPhaseRetryWithContentRefIgnoresDivergentCallerBody is
// the content_ref-storage variant of the above: the durable body lives
// behind a content_ref, not inline, so the fix must resolve it back out of
// the content store (not merely read an inline column) before applying it
// to current_state.
func TestRestore_ArchivedPhaseRetryWithContentRefIgnoresDivergentCallerBody(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 1)

	receipt, err := store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	original, err := resolveArchiveRef(ctx, store.db, ws, receipt.CompactionID, "ra")
	require.NoError(t, err)

	stuck, err := store.appendReactivationMutation(ctx, ws, token, "restore-4", "ra", KindFollowUp, original, StorageContentRef)
	require.NoError(t, err)
	require.Equal(t, "archived", stuck.Phase)

	divergent := map[string]any{"resolution": "content-ref-divergent-retry-body"}
	resumed, err := store.ReactivateRecord(ctx, ws, token, "restore-4", "ra", KindFollowUp, divergent, StorageContentRef)
	require.NoError(t, err)
	require.Equal(t, "committed", resumed.Phase)

	rec, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, original, rec.Body, "archived-phase retry must resolve and apply the content_ref-backed logged body, not the caller's divergent retry body")
}
