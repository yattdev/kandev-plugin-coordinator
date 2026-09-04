package durablestate

import (
	"context"
	"encoding/json"
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

// TestCompaction_RecoveryFailsClosedWhenArchiveContentMissing simulates a
// receipt stuck in phase "archived" (crashed between §5 steps (c) and (d))
// whose backing archive row has since gone missing (e.g. corruption,
// accidental deletion, a substituted/incomplete archive). Recovery
// (ResumeCompactions -> finishCompactionSwap) must fail closed: it must
// not delete the still-live current_state row and must not mark the
// receipt "committed", because doing so would silently lose the only
// durable copy of the rolled record's body.
func TestCompaction_RecoveryFailsClosedWhenArchiveContentMissing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-missing-archive", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)

	// Simulate the archive content going missing between the crash and
	// recovery (corruption, disk issue, accidental deletion, etc).
	res, err := store.db.ExecContext(ctx,
		`DELETE FROM archive WHERE workspace_id = ? AND compaction_id = ? AND record_id = ?`, ws, "compaction-missing-archive", "ra")
	require.NoError(t, err)
	affected, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	_, err = store.ResumeCompactions(ctx, ws, token)
	require.Error(t, err, "recovery must fail closed when the archive content backing a rolled record is missing")

	// current_state must be untouched: the record must still be live, the
	// receipt must still be stuck in "archived" (never incorrectly
	// promoted to "committed").
	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found, "current_state must not be mutated when archive revalidation fails")

	stillArchived, err := store.getCompactionReceipt(ctx, ws, "compaction-missing-archive")
	require.NoError(t, err)
	require.Equal(t, "archived", stillArchived.Phase, "the receipt must not be committed when its backing archive content failed revalidation")

	health, err := store.GetHealth(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, int64(1), health.CompactionRecoveryVerificationFailures)
}

// TestCompaction_RecoveryFailsClosedWhenCorrelatedMutationDeleted
// simulates a receipt stuck in phase "archived" whose correlated
// remove-op mutation-log entry (the durable record of which record_ids
// this compaction_id rolled) has since been deleted. Recovery must fail
// closed rather than commit a current-state swap that no longer
// correlates to any mutation-log entry.
func TestCompaction_RecoveryFailsClosedWhenCorrelatedMutationDeleted(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-missing-mutation", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)

	// Simulate the correlated remove-op mutation-log entry being deleted
	// between the crash and recovery.
	res, err := store.db.ExecContext(ctx,
		`DELETE FROM mutation_log WHERE workspace_id = ? AND compaction_id = ? AND record_id = ?`, ws, "compaction-missing-mutation", "ra")
	require.NoError(t, err)
	affected, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	_, err = store.ResumeCompactions(ctx, ws, token)
	require.Error(t, err, "recovery must fail closed when the receipt's correlated mutation-log entry is missing")

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found, "current_state must not be mutated when mutation-log correlation revalidation fails")

	stillArchived, err := store.getCompactionReceipt(ctx, ws, "compaction-missing-mutation")
	require.NoError(t, err)
	require.Equal(t, "archived", stillArchived.Phase)

	health, err := store.GetHealth(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, int64(1), health.CompactionRecoveryVerificationFailures)
}

// TestCompaction_RecoveryFailsClosedWhenCorrelatedMutationSubstituted
// simulates a receipt stuck in phase "archived" whose correlated
// remove-op mutation-log entry has had its before_ref/before_sha256
// substituted -- while its workspace_id, compaction_id, and record_id are
// left untouched, and the substituted ref/sha256 pair remains internally
// self-consistent (points at a real, differently-archived body whose hash
// matches the substituted sha256). CheckCompactionCorrelation's
// record_id-set comparison alone cannot catch this: both sets still agree
// on "ra". Recovery must independently re-derive the mutation's linkage to
// *this* compaction's own archive-append location and content -- not just
// trust that a same-shaped remove entry with the right ids exists -- and
// fail closed rather than commit a current-state swap correlated to
// substituted evidence.
func TestCompaction_RecoveryFailsClosedWhenCorrelatedMutationSubstituted(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-substituted", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)

	// Archive a second, unrelated compaction's record so the substituted
	// before_ref/before_sha256 point at real, hash-consistent archived
	// content -- just not the content this receipt's rollup itself wrote.
	_, err = store.archiveCompaction(ctx, ws, token, "compaction-other", []RolledRecordInput{{RecordID: "rb", ResolvedAt: nowUTC()}})
	require.NoError(t, err)

	var otherSHA string
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT sha256 FROM archive WHERE workspace_id = ? AND compaction_id = ? AND record_id = ?`,
		ws, "compaction-other", "rb",
	).Scan(&otherSHA))

	res, err := store.db.ExecContext(ctx,
		`UPDATE mutation_log SET before_ref = ?, before_sha256 = ?
		 WHERE workspace_id = ? AND compaction_id = ? AND record_id = ?`,
		"archive:compaction-other:rb", otherSHA, ws, "compaction-substituted", "ra")
	require.NoError(t, err)
	affected, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	_, err = store.ResumeCompactions(ctx, ws, token)
	require.Error(t, err, "recovery must fail closed when the correlated remove mutation's before_ref/before_sha256 have been substituted, even when internally hash-consistent")

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found, "current_state must not be mutated when mutation-linkage revalidation fails")

	stillArchived, err := store.getCompactionReceipt(ctx, ws, "compaction-substituted")
	require.NoError(t, err)
	require.Equal(t, "archived", stillArchived.Phase, "the receipt must not be committed when its mutation linkage failed revalidation")
}

func TestCompaction_RecoveryFailsClosedWhenCorrelatedMutationDuplicated(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-duplicate-mutation", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)

	var archiveSHA string
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT sha256 FROM archive WHERE workspace_id = ? AND compaction_id = ? AND record_id = ?`,
		ws, "compaction-duplicate-mutation", "ra",
	).Scan(&archiveSHA))

	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error {
		mutationID, err := nextMutationID(ctx, tx, ws)
		if err != nil {
			return err
		}
		return writeMutationRow(ctx, tx, Mutation{
			MutationID:   mutationID,
			WorkspaceID:  ws,
			Timestamp:    nowUTC(),
			Op:           OpRemove,
			RecordID:     "ra",
			RecordKind:   KindFollowUp,
			Before:       &PayloadSide{Storage: StorageContentRef, SHA256: archiveSHA, Ref: "archive:compaction-duplicate-mutation:ra"},
			CompactionID: "compaction-duplicate-mutation",
			FencingToken: token,
		})
	}))

	_, err = store.ResumeCompactions(ctx, ws, token)
	require.Error(t, err, "recovery must reject duplicate remove rows for one rolled record")

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found, "current_state must not be mutated when duplicate mutation correlation fails")
}

func TestCompaction_RecoveryFailsClosedWhenCorrelatedMutationExtra(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-extra-mutation", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)

	var archiveSHA string
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT sha256 FROM archive WHERE workspace_id = ? AND compaction_id = ? AND record_id = ?`,
		ws, "compaction-extra-mutation", "ra",
	).Scan(&archiveSHA))

	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error {
		mutationID, err := nextMutationID(ctx, tx, ws)
		if err != nil {
			return err
		}
		return writeMutationRow(ctx, tx, Mutation{
			MutationID:   mutationID,
			WorkspaceID:  ws,
			Timestamp:    nowUTC(),
			Op:           OpRemove,
			RecordID:     "rb",
			RecordKind:   KindFollowUp,
			Before:       &PayloadSide{Storage: StorageContentRef, SHA256: archiveSHA, Ref: "archive:compaction-extra-mutation:ra"},
			CompactionID: "compaction-extra-mutation",
			FencingToken: token,
		})
	}))

	_, err = store.ResumeCompactions(ctx, ws, token)
	require.Error(t, err, "recovery must reject extra remove rows not named by rolled_records")

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found, "current_state must not be mutated when extra mutation correlation fails")
}

func TestCompaction_RecoveryFailsClosedWhenReceiptMutationIDSubstituted(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-mutid-substituted", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)

	tamperedRolled := append([]RolledRecord(nil), receipt.RolledRecords...)
	tamperedRolled[0].MutationID += 100
	encoded, err := json.Marshal(tamperedRolled)
	require.NoError(t, err)
	res, err := store.db.ExecContext(ctx,
		`UPDATE compaction_receipts SET rolled_records = ? WHERE workspace_id = ? AND compaction_id = ?`,
		string(encoded), ws, "compaction-mutid-substituted")
	require.NoError(t, err)
	affected, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	_, err = store.ResumeCompactions(ctx, ws, token)
	require.Error(t, err, "recovery must reject a receipt whose rolled_records mutation_id no longer matches the durable remove row")

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found, "current_state must not be mutated when receipt mutation_id correlation fails")
}

func TestCompaction_MutationsByCompactionIDAreDeterministicallyOrdered(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"n": float64(1)}

	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error {
		if err := writeMutationRow(ctx, tx, Mutation{
			MutationID:   20,
			WorkspaceID:  ws,
			Timestamp:    nowUTC(),
			Op:           OpRemove,
			RecordID:     "r20",
			RecordKind:   KindFollowUp,
			Before:       mkInlineSide(t, body),
			CompactionID: "c-order",
			FencingToken: 1,
		}); err != nil {
			return err
		}
		return writeMutationRow(ctx, tx, Mutation{
			MutationID:   10,
			WorkspaceID:  ws,
			Timestamp:    nowUTC(),
			Op:           OpRemove,
			RecordID:     "r10",
			RecordKind:   KindFollowUp,
			Before:       mkInlineSide(t, body),
			CompactionID: "c-order",
			FencingToken: 1,
		})
	}))

	mutations, err := getMutationsByCompactionID(ctx, store.db, ws, "c-order")
	require.NoError(t, err)
	require.Equal(t, []int64{10, 20}, []int64{mutations[0].MutationID, mutations[1].MutationID})
}

// TestCompaction_RecoveryFailsClosedWhenArchiveBodyTampered simulates the
// archive row still existing but its body having been substituted after
// the crash (e.g. a torn write, disk corruption). The row's own stored
// sha256 no longer matches a hash recomputed from its (tampered) body, so
// recovery must fail closed rather than trust the row at face value.
func TestCompaction_RecoveryFailsClosedWhenArchiveBodyTampered(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 3)

	receipt, err := store.archiveCompaction(ctx, ws, token, "compaction-tampered", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)
	require.Equal(t, "archived", receipt.Phase)

	tampered, err := marshalBody(map[string]any{"n": float64(999)})
	require.NoError(t, err)
	res, err := store.db.ExecContext(ctx,
		`UPDATE archive SET body = ? WHERE workspace_id = ? AND compaction_id = ? AND record_id = ?`,
		tampered, ws, "compaction-tampered", "ra")
	require.NoError(t, err)
	affected, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), affected)

	_, err = store.ResumeCompactions(ctx, ws, token)
	require.Error(t, err, "recovery must fail closed when an archive row's body no longer matches its own stored sha256")

	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found, "current_state must not be mutated when archive content hash verification fails")

	stillArchived, err := store.getCompactionReceipt(ctx, ws, "compaction-tampered")
	require.NoError(t, err)
	require.Equal(t, "archived", stillArchived.Phase)
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

func TestCompaction_RejectsCompactionIDCollidingWithRestoreReceipt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "compaction-restore-id-collision"
	token, err := store.AcquireLease(ctx, ws, "leader")
	require.NoError(t, err)
	_, err = store.ReactivateRecord(ctx, ws, token, "shared-id", "ra", KindFollowUp, map[string]any{"open": true}, StorageInline)
	require.NoError(t, err)

	_, err = store.Compact(ctx, ws, token, "shared-id", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.ErrorContains(t, err, "collides with existing receipt kind")
	_, found, err := store.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.True(t, found)
}
