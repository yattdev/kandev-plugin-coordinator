package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifySetEquality_FullReplayThenSetEqualityMatchesReceipt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	r1, r2, r3 := map[string]any{"n": float64(1)}, map[string]any{"n": float64(2)}, map[string]any{"n": float64(3)}
	preState := map[string]map[string]any{"r1": r1, "r2": r2, "r3": r3}
	log := []Mutation{
		{MutationID: 10, WorkspaceID: ws, Timestamp: "t10", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, r1), CompactionID: "c-9", FencingToken: 5},
		{MutationID: 11, WorkspaceID: ws, Timestamp: "t11", Op: OpRemove, RecordID: "r2", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, r2), CompactionID: "c-9", FencingToken: 5},
	}
	receipt := &CompactionReceipt{CompactionID: "c-9", WorkspaceID: ws, RolledRecords: []RolledRecord{{RecordID: "r1"}, {RecordID: "r2"}}, Phase: "committed"}
	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error { return insertCompactionReceipt(ctx, tx, receipt) }))

	postState, err := store.replayMutations(ctx, ws, preState, log, ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]map[string]any{"r3": r3}, postState)

	preIDs := []string{"r1", "r2", "r3"}
	postIDs := []string{"r3"}
	require.NoError(t, VerifySetEquality(preIDs, postIDs, []string{"r1", "r2"}))
}

func TestVerifySetEquality_RejectsLostRecord(t *testing.T) {
	err := VerifySetEquality([]string{"r1", "r2", "r3"}, []string{"r3"}, []string{"r1"})
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestVerifySetEquality_RejectsDoubleCountedRecord(t *testing.T) {
	err := VerifySetEquality([]string{"r1", "r2"}, []string{"r1", "r2"}, []string{"r2"})
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestReplay_RestoreToArbitraryTimestampBetweenMutations(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	bodyV1 := map[string]any{"status": "open"}
	bodyV2 := map[string]any{"status": "resolved"}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "2026-09-03T00:00:01Z", Op: OpAdd, RecordID: "f1", RecordKind: KindFollowUp,
			After: mkInlineSide(t, bodyV1), FencingToken: 1},
		{MutationID: 2, WorkspaceID: ws, Timestamp: "2026-09-03T00:05:00Z", Op: OpUpdate, RecordID: "f1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, bodyV1), After: mkInlineSide(t, bodyV2), FencingToken: 1},
	}
	stateAtT, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{TargetTimestamp: "2026-09-03T00:02:00Z"})
	require.NoError(t, err)
	require.Equal(t, bodyV1, stateAtT["f1"])
}

func TestRetention_PrunableSnapshotIDsKeepsNewestN(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)
	var snapshotIDs []string
	for i := 1; i <= 12; i++ {
		snap, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
		require.NoError(t, err)
		snapshotIDs = append(snapshotIDs, snap.SnapshotID)
	}
	prunable, err := store.PrunableSnapshotIDs(ctx, ws, 10)
	require.NoError(t, err)
	require.Len(t, prunable, 2)
	// The oldest two captured snapshots are exactly the ones outside the
	// newest-10 window.
	require.ElementsMatch(t, snapshotIDs[:2], prunable)
}

func TestRetention_PrunableMutationIDsRespectsOldestRetainedWatermark(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)

	for i := 0; i < 9; i++ {
		_, err := store.AppendAdd(ctx, ws, token, recIDFor(i), KindDirtyTask, map[string]any{"n": float64(i)}, StorageInline)
		require.NoError(t, err)
	}

	// Build fabricated snapshot rows with explicit watermarks 5 and 8,
	// mirroring the reference test's retained_snapshots input directly.
	watermark5 := int64(5)
	watermark8 := int64(8)
	s1 := &Snapshot{SnapshotID: "s1", WorkspaceID: ws, Timestamp: "t-s1", Trigger: TriggerScheduledCadence, Content: map[string]map[string]map[string]any{}, MutationLogWatermark: &watermark5, FencingToken: token}
	s2 := &Snapshot{SnapshotID: "s2", WorkspaceID: ws, Timestamp: "t-s2", Trigger: TriggerScheduledCadence, Content: map[string]map[string]map[string]any{}, MutationLogWatermark: &watermark8, FencingToken: token}
	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error {
		if err := insertSnapshot(ctx, tx, s1); err != nil {
			return err
		}
		return insertSnapshot(ctx, tx, s2)
	}))

	prunable, err := store.PrunableMutationIDs(ctx, ws, []string{"s1", "s2"})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3, 4, 5}, prunable)
}

func TestRetention_PrunableMutationIDsEmptyWhenNoWatermark(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)
	_, err = store.AppendAdd(ctx, ws, token, "r1", KindDirtyTask, map[string]any{"n": float64(1)}, StorageInline)
	require.NoError(t, err)

	s1 := &Snapshot{SnapshotID: "s1", WorkspaceID: ws, Timestamp: "t-s1", Trigger: TriggerScheduledCadence, Content: map[string]map[string]map[string]any{}, MutationLogWatermark: nil, FencingToken: token}
	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error { return insertSnapshot(ctx, tx, s1) }))

	prunable, err := store.PrunableMutationIDs(ctx, ws, []string{"s1"})
	require.NoError(t, err)
	require.Empty(t, prunable)
}

func recIDFor(i int) string {
	return "r" + string(rune('a'+i))
}
