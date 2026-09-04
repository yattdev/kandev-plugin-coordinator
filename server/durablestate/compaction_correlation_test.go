package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyMutation_RemoveWithoutCompactionIDIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	body := map[string]any{"n": float64(1)}
	state := map[string]map[string]any{"r1": body}
	m := Mutation{MutationID: 1, WorkspaceID: "w1", Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindDirtyTask,
		Before: mkInlineSide(t, body), After: nil, CompactionID: "", FencingToken: 1}
	_, err := applyMutationInMemory(ctx, store.db, "w1", state, m)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestApplyMutation_AddWithNonNullCompactionIDIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	body := map[string]any{"n": float64(1)}
	m := Mutation{MutationID: 1, WorkspaceID: "w1", Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindDirtyTask,
		After: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 1}
	_, err := applyMutationInMemory(ctx, store.db, "w1", map[string]map[string]any{}, m)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestApplyMutation_UpdateWithNonNullCompactionIDIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	body1 := map[string]any{"n": float64(1)}
	body2 := map[string]any{"n": float64(2)}
	m := Mutation{MutationID: 1, WorkspaceID: "w1", Timestamp: "t1", Op: OpUpdate, RecordID: "r1", RecordKind: KindDirtyTask,
		Before: mkInlineSide(t, body1), After: mkInlineSide(t, body2), CompactionID: "c-1", FencingToken: 1}
	_, err := applyMutationInMemory(ctx, store.db, "w1", map[string]map[string]any{"r1": body1}, m)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestCheckCompactionCorrelation_ReceiptMatchesLogRemoveEntries(t *testing.T) {
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: "w1", Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
		{MutationID: 2, WorkspaceID: "w1", Timestamp: "t2", Op: OpRemove, RecordID: "r2", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
	}
	receipt := &CompactionReceipt{CompactionID: "c-1", RolledRecords: []RolledRecord{{RecordID: "r1", MutationID: 1}, {RecordID: "r2", MutationID: 2}}}
	require.NoError(t, CheckCompactionCorrelation(receipt, log))
}

func TestCheckCompactionCorrelation_MismatchIsRejected(t *testing.T) {
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: "w1", Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
	}
	// Receipt claims r1 AND r2 were rolled, but the log only shows r1.
	receipt := &CompactionReceipt{CompactionID: "c-1", RolledRecords: []RolledRecord{{RecordID: "r1", MutationID: 1}, {RecordID: "r2", MutationID: 2}}}
	err := CheckCompactionCorrelation(receipt, log)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestCheckCompactionCorrelation_DuplicateLogRecordIDIsRejected(t *testing.T) {
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 2, WorkspaceID: "w1", Timestamp: "t2", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
		{MutationID: 1, WorkspaceID: "w1", Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
	}
	receipt := &CompactionReceipt{CompactionID: "c-1", RolledRecords: []RolledRecord{{RecordID: "r1", MutationID: 1}}}
	err := CheckCompactionCorrelation(receipt, log)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
	require.Contains(t, err.Error(), "duplicate remove mutation entries")
}

func TestCheckCompactionCorrelation_ExtraLogRecordIsRejected(t *testing.T) {
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: "w1", Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
		{MutationID: 2, WorkspaceID: "w1", Timestamp: "t2", Op: OpRemove, RecordID: "r2", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
	}
	receipt := &CompactionReceipt{CompactionID: "c-1", RolledRecords: []RolledRecord{{RecordID: "r1", MutationID: 1}}}
	err := CheckCompactionCorrelation(receipt, log)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
	require.Contains(t, err.Error(), "extra=[r2]")
}

func TestCheckCompactionCorrelation_MutationIDMismatchIsRejected(t *testing.T) {
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 2, WorkspaceID: "w1", Timestamp: "t2", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
	}
	receipt := &CompactionReceipt{CompactionID: "c-1", RolledRecords: []RolledRecord{{RecordID: "r1", MutationID: 1}}}
	err := CheckCompactionCorrelation(receipt, log)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
	require.Contains(t, err.Error(), "declares mutation_id 1")
}

func TestCheckCompactionCorrelation_DuplicateReceiptRecordIDIsRejected(t *testing.T) {
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: "w1", Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 3},
	}
	receipt := &CompactionReceipt{CompactionID: "c-1", RolledRecords: []RolledRecord{{RecordID: "r1", MutationID: 1}, {RecordID: "r1", MutationID: 2}}}
	err := CheckCompactionCorrelation(receipt, log)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
	require.Contains(t, err.Error(), "duplicate rolled record_id")
}

// TestReplayCompactionReceiptCorrelation exercises replay()'s own
// correlation enforcement (not the standalone CheckCompactionCorrelation
// helper) against a real Store, mirroring
// TestReplayCompactionReceiptCorrelation in test_replay_reference.py.
func TestReplay_AbsentReceiptIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 1},
	}
	// No receipt inserted at all.
	_, err := store.replayMutations(ctx, ws, map[string]map[string]any{"r1": body}, log, ReplayOptions{})
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestReplay_NonexistentCompactionIDIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 1},
	}
	other := &CompactionReceipt{CompactionID: "c-999", WorkspaceID: ws, RolledRecords: []RolledRecord{{RecordID: "r1", MutationID: 1}}, Phase: "committed"}
	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error { return insertCompactionReceipt(ctx, tx, other) }))
	_, err := store.replayMutations(ctx, ws, map[string]map[string]any{"r1": body}, log, ReplayOptions{})
	require.Error(t, err)
}

func TestReplay_SubstitutedMismatchedReceiptIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpRemove, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, body), CompactionID: "c-1", FencingToken: 1},
	}
	mismatched := &CompactionReceipt{CompactionID: "c-1", WorkspaceID: ws, RolledRecords: []RolledRecord{{RecordID: "some-other-record", MutationID: 1}}, Phase: "committed"}
	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error { return insertCompactionReceipt(ctx, tx, mismatched) }))
	_, err := store.replayMutations(ctx, ws, map[string]map[string]any{"r1": body}, log, ReplayOptions{})
	require.Error(t, err)
}

func TestReplay_CorrelatedRemovalSucceedsAndPreservesOrderChecks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	bodyV1 := map[string]any{"n": float64(1)}
	bodyV2 := map[string]any{"n": float64(2)}
	// Fed out of mutation_id order (2 before 1) -- replay must still apply
	// mutation_id 1 (the remove) before mutation_id 2 (the update).
	log := []Mutation{
		{MutationID: 2, WorkspaceID: ws, Timestamp: "t2", Op: OpUpdate, RecordID: "r1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, bodyV1), After: mkInlineSide(t, bodyV2), FencingToken: 1},
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpRemove, RecordID: "r2", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, bodyV1), CompactionID: "c-1", FencingToken: 1},
	}
	receipt := &CompactionReceipt{CompactionID: "c-1", WorkspaceID: ws, RolledRecords: []RolledRecord{{RecordID: "r2", MutationID: 1}}, Phase: "committed"}
	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error { return insertCompactionReceipt(ctx, tx, receipt) }))

	state, err := store.replayMutations(ctx, ws, map[string]map[string]any{"r1": bodyV1, "r2": bodyV1}, log, ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, map[string]map[string]any{"r1": bodyV2}, state)
}

func TestReplay_ReceiptNotRequiredForAddOrUpdateOnlyLog(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"n": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindFollowUp,
			After: mkInlineSide(t, body), FencingToken: 1},
	}
	state, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, body, state["r1"])
}
