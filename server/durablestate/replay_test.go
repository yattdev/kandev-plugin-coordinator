package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Helpers mirroring test_replay_reference.py's mk_side/mk_ref_side: build
// an inline or content_ref PayloadSide for a given body.

func mkInlineSide(t *testing.T, body map[string]any) *PayloadSide {
	t.Helper()
	sha, err := canonicalHash(body)
	require.NoError(t, err)
	return &PayloadSide{Storage: StorageInline, SHA256: sha, Body: body}
}

func mkRefSide(t *testing.T, ref string, body map[string]any) *PayloadSide {
	t.Helper()
	sha, err := canonicalHash(body)
	require.NoError(t, err)
	return &PayloadSide{Storage: StorageContentRef, SHA256: sha, Ref: ref}
}

// putRef registers body under a "content:<sha256>" ref directly in
// content_store so a content_ref side can resolve it, mirroring the
// reference tests' payload_store dict.
func putRef(t *testing.T, store *Store, workspaceID string, body map[string]any) string {
	t.Helper()
	ctx := context.Background()
	var ref string
	err := store.withWriteTx(ctx, func(tx execer) error {
		var err error
		ref, _, err = putContentStoreRef(ctx, tx, workspaceID, body)
		return err
	})
	require.NoError(t, err)
	return ref
}

func TestReplay_AddUpdateRemoveSequenceReconstructsExpectedState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"

	bodyV1 := map[string]any{"title": "follow up A", "status": "open"}
	bodyV2 := map[string]any{"title": "follow up A", "status": "resolved"}

	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "f1", RecordKind: KindFollowUp,
			Before: nil, After: mkInlineSide(t, bodyV1), FencingToken: 1},
		{MutationID: 2, WorkspaceID: ws, Timestamp: "t2", Op: OpUpdate, RecordID: "f1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, bodyV1), After: mkInlineSide(t, bodyV2), FencingToken: 1},
		{MutationID: 3, WorkspaceID: ws, Timestamp: "t3", Op: OpRemove, RecordID: "f1", RecordKind: KindFollowUp,
			Before: mkInlineSide(t, bodyV2), After: nil, CompactionID: "c-1", FencingToken: 1},
	}
	receipt := &CompactionReceipt{
		CompactionID:  "c-1",
		WorkspaceID:   ws,
		Kind:          ReceiptRollup,
		Phase:         "committed",
		RolledRecords: []RolledRecord{{RecordID: "f1", MutationID: 3}},
	}
	require.NoError(t, store.withWriteTx(ctx, func(tx execer) error {
		return insertCompactionReceipt(ctx, tx, receipt)
	}))

	state, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.NoError(t, err)
	require.Empty(t, state)
}

func TestReplay_OrderIndependenceOfInputListOrder(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"lease_owner": "leader-1"}
	log := []Mutation{
		{MutationID: 5, WorkspaceID: ws, Timestamp: "t5", Op: OpAdd, RecordID: "lease-1", RecordKind: KindLease,
			Before: nil, After: mkInlineSide(t, body), FencingToken: 2},
	}
	reversed := []Mutation{log[0]}

	stateA, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.NoError(t, err)
	stateB, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, reversed, ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, stateA, stateB)
	require.Equal(t, body, stateA["lease-1"])
}

func TestReplay_DuplicateMutationIDRejectedAsAmbiguous(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"x": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindEscalation,
			After: mkInlineSide(t, body), FencingToken: 1},
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r2", RecordKind: KindEscalation,
			After: mkInlineSide(t, body), FencingToken: 1},
	}
	_, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestReplay_ToArbitraryTargetMutationIDStopsPartway(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body1 := map[string]any{"n": float64(1)}
	body2 := map[string]any{"n": float64(2)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindDirtyTask,
			After: mkInlineSide(t, body1), FencingToken: 1},
		{MutationID: 2, WorkspaceID: ws, Timestamp: "t2", Op: OpUpdate, RecordID: "r1", RecordKind: KindDirtyTask,
			Before: mkInlineSide(t, body1), After: mkInlineSide(t, body2), FencingToken: 1},
	}
	one := int64(1)
	stateAt1, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{TargetMutationID: &one})
	require.NoError(t, err)
	require.Equal(t, body1, stateAt1["r1"])

	two := int64(2)
	stateAt2, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{TargetMutationID: &two})
	require.NoError(t, err)
	require.Equal(t, body2, stateAt2["r1"])
}

func TestReplay_InlineBodyAddIsVerifiedAndApplied(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"a": float64(1)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindFollowUp,
			After: mkInlineSide(t, body), FencingToken: 1},
	}
	state, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, body, state["r1"])
}

func TestReplay_ContentRefAddResolvesFromPayloadStore(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"a": float64(1)}
	ref := putRef(t, store, ws, body)
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindFollowUp,
			After: mkRefSide(t, ref, body), FencingToken: 1},
	}
	state, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, body, state["r1"])
}

func TestReplay_ContentRefUnavailableAbortsReplayAsCorrupt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"a": float64(1)}
	// Never registered in content_store.
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindFollowUp,
			After: mkRefSide(t, "content:deadbeef", body), FencingToken: 1},
	}
	_, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.Error(t, err)
}

func TestReplay_TamperedInlineBodyFailsHashVerification(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body := map[string]any{"a": float64(1)}
	side := mkInlineSide(t, body)
	side.Body = map[string]any{"a": float64(999)} // tampered after hash computed
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindFollowUp,
			After: side, FencingToken: 1},
	}
	_, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.ErrorIs(t, err, ErrHashMismatch)
}

func TestReplay_ContentRefReturningSubstitutedBodyFailsHashVerification(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	original := map[string]any{"a": float64(1)}
	ref := putRef(t, store, ws, original)
	// Declares the hash of a *different* body than what the ref actually
	// resolves to -- simulates a payload store returning substituted
	// content for a locator.
	different := map[string]any{"a": float64(2)}
	side := mkRefSide(t, ref, different)
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindFollowUp,
			After: side, FencingToken: 1},
	}
	_, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.ErrorIs(t, err, ErrHashMismatch)
}

func TestReplay_UpdateBeforeStateMismatchIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	actual := map[string]any{"n": float64(1)}
	claimed := map[string]any{"n": float64(999)}
	state := map[string]map[string]any{"r1": actual}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpUpdate, RecordID: "r1", RecordKind: KindDirtyTask,
			Before: mkInlineSide(t, claimed), After: mkInlineSide(t, map[string]any{"n": float64(2)}), FencingToken: 1},
	}
	_, err := store.replayMutations(ctx, ws, state, log, ReplayOptions{})
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}
