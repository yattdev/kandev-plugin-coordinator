package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCrashRetry_DuplicateReapplicationOfSameMutationIsRejected(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	bodyV1 := map[string]any{"n": float64(1)}
	bodyV2 := map[string]any{"n": float64(2)}
	mutation := Mutation{MutationID: 2, WorkspaceID: "w1", Timestamp: "t2", Op: OpUpdate, RecordID: "r1", RecordKind: KindDirtyTask,
		Before: mkInlineSide(t, bodyV1), After: mkInlineSide(t, bodyV2), FencingToken: 1}
	state := map[string]map[string]any{"r1": bodyV1}

	afterFirst, err := applyMutationInMemory(ctx, store.db, "w1", state, mutation)
	require.NoError(t, err)
	require.Equal(t, bodyV2, afterFirst["r1"])

	// Re-applying the same mutation against the already-updated state must
	// fail: the working state is now body_v2, but the mutation's `before`
	// still declares body_v1.
	_, err = applyMutationInMemory(ctx, store.db, "w1", afterFirst, mutation)
	require.Error(t, err)
	require.IsType(t, &ReplayError{}, err)
}

func TestCrashRetry_ResumeFromCheckpointReappliesOnlyRemainingMutations(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	body1 := map[string]any{"n": float64(1)}
	body2 := map[string]any{"n": float64(2)}
	body3 := map[string]any{"n": float64(3)}
	log := []Mutation{
		{MutationID: 1, WorkspaceID: ws, Timestamp: "t1", Op: OpAdd, RecordID: "r1", RecordKind: KindDirtyTask,
			After: mkInlineSide(t, body1), FencingToken: 1},
		{MutationID: 2, WorkspaceID: ws, Timestamp: "t2", Op: OpUpdate, RecordID: "r1", RecordKind: KindDirtyTask,
			Before: mkInlineSide(t, body1), After: mkInlineSide(t, body2), FencingToken: 1},
		{MutationID: 3, WorkspaceID: ws, Timestamp: "t3", Op: OpUpdate, RecordID: "r1", RecordKind: KindDirtyTask,
			Before: mkInlineSide(t, body2), After: mkInlineSide(t, body3), FencingToken: 1},
	}
	fullReplay, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{})
	require.NoError(t, err)

	checkpointK := int64(2)
	stateAtCheckpoint, err := store.replayMutations(ctx, ws, map[string]map[string]any{}, log, ReplayOptions{TargetMutationID: &checkpointK})
	require.NoError(t, err)

	var remaining []Mutation
	for _, m := range log {
		if m.MutationID > checkpointK {
			remaining = append(remaining, m)
		}
	}
	resumed, err := store.replayMutations(ctx, ws, stateAtCheckpoint, remaining, ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, fullReplay, resumed)
}

func TestCrashRetry_ReplayFromCheckpointAgainstRealStore(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 1) // ra

	m2, err := store.AppendUpdate(ctx, ws, token, "ra", map[string]any{"n": float64(99)}, StorageInline)
	require.NoError(t, err)
	m3, err := store.AppendUpdate(ctx, ws, token, "ra", map[string]any{"n": float64(100)}, StorageInline)
	require.NoError(t, err)

	snap, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
	require.NoError(t, err)

	m4, err := store.AppendUpdate(ctx, ws, token, "ra", map[string]any{"n": float64(101)}, StorageInline)
	require.NoError(t, err)
	m5, err := store.AppendUpdate(ctx, ws, token, "ra", map[string]any{"n": float64(102)}, StorageInline)
	require.NoError(t, err)
	_ = m2
	_ = m3

	// First call has no checkpoint yet: replay from the snapshot's
	// watermark forward, applying only m4, and save a checkpoint at m4.
	targetM4 := m4.MutationID
	stateAtM4, err := store.ReplayFromCheckpoint(ctx, ws, snap.SnapshotID, "ckpt-1", ReplayOptions{TargetMutationID: &targetM4})
	require.NoError(t, err)
	require.Equal(t, float64(101), stateAtM4["ra"]["n"])

	checkpointed, ok, err := store.LoadReplayCheckpoint(ctx, ws, "ckpt-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, m4.MutationID, checkpointed)

	// Resuming applies only m5 on top of the checkpointed state, reaching
	// the same result full replay from the snapshot would.
	final, err := store.ReplayFromCheckpoint(ctx, ws, snap.SnapshotID, "ckpt-1", ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, float64(102), final["ra"]["n"])

	fullReplay, err := store.Replay(ctx, ws, snap.SnapshotID, ReplayOptions{})
	require.NoError(t, err)
	require.Equal(t, fullReplay["ra"], final["ra"])
	_ = m5
}

func TestCrashRetry_ReusedCheckpointRebuildsForEarlierMutationTarget(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "checkpoint-earlier-mutation"
	token, err := store.AcquireLease(ctx, ws, "leader")
	require.NoError(t, err)
	snap, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
	require.NoError(t, err)

	_, err = store.AppendAdd(ctx, ws, token, "r1", KindDirtyTask, map[string]any{"n": int64(1)}, StorageInline)
	require.NoError(t, err)
	m2, err := store.AppendUpdate(ctx, ws, token, "r1", map[string]any{"n": int64(2)}, StorageInline)
	require.NoError(t, err)
	m3, err := store.AppendUpdate(ctx, ws, token, "r1", map[string]any{"n": int64(3)}, StorageInline)
	require.NoError(t, err)
	require.NoError(t, store.SaveReplayCheckpoint(ctx, ws, "ckpt", m3.MutationID))

	state, err := store.ReplayFromCheckpoint(ctx, ws, snap.SnapshotID, "ckpt", ReplayOptions{TargetMutationID: &m2.MutationID})
	require.NoError(t, err)
	require.Equal(t, int64(2), state["r1"]["n"])
	checkpoint, ok, err := store.LoadReplayCheckpoint(ctx, ws, "ckpt")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, m3.MutationID, checkpoint, "historical replay must not move the durable checkpoint backward")
}

func TestCrashRetry_ReusedCheckpointRebuildsForEarlierTimestampTarget(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "checkpoint-earlier-timestamp"
	token, err := store.AcquireLease(ctx, ws, "leader")
	require.NoError(t, err)
	snap, err := store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
	require.NoError(t, err)

	m1, err := store.AppendAdd(ctx, ws, token, "r1", KindDirtyTask, map[string]any{"n": int64(1)}, StorageInline)
	require.NoError(t, err)
	m2, err := store.AppendUpdate(ctx, ws, token, "r1", map[string]any{"n": int64(2)}, StorageInline)
	require.NoError(t, err)
	m3, err := store.AppendUpdate(ctx, ws, token, "r1", map[string]any{"n": int64(3)}, StorageInline)
	require.NoError(t, err)
	timestamps := map[int64]string{
		m1.MutationID: "2026-09-04T00:00:01Z",
		m2.MutationID: "2026-09-04T00:00:02Z",
		m3.MutationID: "2026-09-04T00:00:03Z",
	}
	for mutationID, timestamp := range timestamps {
		_, err := store.db.ExecContext(ctx,
			`UPDATE mutation_log SET timestamp = ? WHERE workspace_id = ? AND mutation_id = ?`,
			timestamp, ws, mutationID)
		require.NoError(t, err)
	}
	require.NoError(t, store.SaveReplayCheckpoint(ctx, ws, "ckpt", m3.MutationID))

	state, err := store.ReplayFromCheckpoint(ctx, ws, snap.SnapshotID, "ckpt", ReplayOptions{TargetTimestamp: timestamps[m2.MutationID]})
	require.NoError(t, err)
	require.Equal(t, int64(2), state["r1"]["n"])
	checkpoint, ok, err := store.LoadReplayCheckpoint(ctx, ws, "ckpt")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, m3.MutationID, checkpoint, "historical replay must not move the durable checkpoint backward")
}
