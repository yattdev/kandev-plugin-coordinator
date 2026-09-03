package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func setupWorkspaceWithRecords(t *testing.T, store *Store, ws string, n int) int64 {
	t.Helper()
	ctx := context.Background()
	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		_, err := store.AppendAdd(ctx, ws, token, recIDFor(i), KindFollowUp, map[string]any{"n": float64(i)}, StorageInline)
		require.NoError(t, err)
	}
	return token
}

func TestCompaction_HashAnchoredSetEqualityAndArchiveVerifiability(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	receipt, err := store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "committed", receipt.Phase)

	// §4: pre_ids == post_ids ∪ rolled_ids, disjoint.
	require.NoError(t, VerifySetEquality(
		[]string{"ra", "rb", "rc"},
		[]string{"rb", "rc"},
		[]string{"ra"},
	))

	// §4: archive_append's rolled_record_id_set_sha256 must be
	// independently recomputable from the actual appended archive rows.
	require.Equal(t, recordIDSetSHA256([]string{"ra"}), receipt.ArchiveAppend.RolledRecordIDSetSHA256)

	var archivedCount int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM archive WHERE workspace_id = ? AND compaction_id = ?`, ws, receipt.CompactionID,
	).Scan(&archivedCount))
	require.Equal(t, 1, archivedCount)
}

func TestCompaction_TornWriteBetweenArchiveAndSwapIsRecoveredOnResume(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	// Simulate a crash between §5 steps (c) and (d): archive the rollup
	// (durable) but never run the current-state swap.
	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-1", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)

	// The archive already has the record; current_state does not yet
	// reflect the removal -- this is the "safe" torn state §5 describes.
	var archivedCount int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM archive WHERE workspace_id = ? AND compaction_id = ?`, ws, "compaction-1",
	).Scan(&archivedCount))
	require.Equal(t, 1, archivedCount)

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found, "current_state must still show the pre-rollup row until step (d) runs")

	health, err := store.GetHealth(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, 1, health.StuckCompactions)

	// Restart: the (new) leader calls ResumeCompactions, which must finish
	// step (d) only, never re-appending the archive.
	resumed, err := store.ResumeCompactions(ctx, ws, token)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	require.Equal(t, "committed", resumed[0].Phase)

	_, found, err = store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM archive WHERE workspace_id = ? AND compaction_id = ?`, ws, "compaction-1",
	).Scan(&archivedCount))
	require.Equal(t, 1, archivedCount, "resume must never duplicate the archive append")

	health, err = store.GetHealth(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, 0, health.StuckCompactions)
	require.Equal(t, int64(1), health.CompactionRecoveries)
}

func TestCompaction_RepeatedCompletionIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 2)

	first, err := store.Compact(ctx, ws, token, "compaction-x", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "committed", first.Phase)

	// Calling Compact again with the same compaction_id (as a retrying
	// leader would after believing the first attempt might have failed)
	// must be a pure no-op: no duplicate archive rows, no error, same
	// receipt content.
	second, err := store.Compact(ctx, ws, token, "compaction-x", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, first.ArchiveAppend.SHA256OfAppendedBytes, second.ArchiveAppend.SHA256OfAppendedBytes)

	var archivedCount int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM archive WHERE workspace_id = ? AND compaction_id = ?`, ws, "compaction-x",
	).Scan(&archivedCount))
	require.Equal(t, 1, archivedCount)
}

func TestCompaction_StaleLeaderCannotCompact(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	oldToken := setupWorkspaceWithRecords(t, store, ws, 2)

	newToken, err := store.AcquireLease(ctx, ws, "leader-new")
	require.NoError(t, err)
	require.Greater(t, newToken, oldToken)

	_, err = store.Compact(ctx, ws, oldToken, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.Error(t, err)
	var fencingErr *FencingError
	require.ErrorAs(t, err, &fencingErr)

	// The current, valid leader can still compact.
	receipt, err := store.Compact(ctx, ws, newToken, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "committed", receipt.Phase)
}

func TestCompaction_ConcurrentCompactionOneLoserDueToStaleFencing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	setupWorkspaceWithRecords(t, store, ws, 4)

	// Two "leaders" race to compact the same workspace; the second
	// election supersedes the first, so a compaction attempt still
	// carrying the first token must lose even if it is issued after the
	// second leader's own attempt already ran (simulating a stale/slow
	// leader's retried write arriving late).
	tokenA, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)
	tokenB, err := store.AcquireLease(ctx, ws, "leader-b")
	require.NoError(t, err)
	require.Greater(t, tokenB, tokenA)

	_, errB := store.Compact(ctx, ws, tokenB, "compaction-b", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, errB)

	_, errA := store.Compact(ctx, ws, tokenA, "compaction-a", []RolledRecordInput{{RecordID: "rb", ResolvedAt: nowUTC()}})
	require.Error(t, errA)
	var fencingErr *FencingError
	require.ErrorAs(t, errA, &fencingErr)
}

func TestCompaction_PreservesUnresolvedRecordsAcrossRollup(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3) // ra, rb, rc

	receipt, err := store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "committed", receipt.Phase)

	remaining, err := store.ListRecords(ctx, ws, "")
	require.NoError(t, err)
	require.Len(t, remaining, 2)
	ids := []string{remaining[0].RecordID, remaining[1].RecordID}
	require.ElementsMatch(t, []string{"rb", "rc"}, ids)
}
