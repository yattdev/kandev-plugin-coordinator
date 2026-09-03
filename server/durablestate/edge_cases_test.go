package durablestate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEdgeCases_AppendAddInputValidation documents the current, intentional
// input-handling behavior of AppendAdd/AppendUpdate at the boundary of the
// narrow internal storage API (§9): recordID/workspaceID are opaque caller-
// supplied identifiers (not validated for non-emptiness by this layer,
// matching the reference engine, which treats them as plain string keys),
// a nil body is accepted and stored as an empty JSON object, and only the
// invariants the spec actually requires — RecordKind enum membership and
// recordID uniqueness per workspace — are enforced.
func TestEdgeCases_AppendAddInputValidation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "qa-ws"

	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)

	// Empty recordID is accepted as an opaque key, not rejected.
	_, err = store.AppendAdd(ctx, ws, token, "", KindFollowUp, map[string]any{"x": 1.0}, StorageInline)
	require.NoError(t, err)

	// Empty workspaceID is likewise accepted as an opaque key.
	_, err = store.AppendAdd(ctx, "", token, "r1", KindFollowUp, map[string]any{"x": 1.0}, StorageInline)
	require.NoError(t, err)

	// A nil body is accepted and canonicalizes/hashes as an empty object.
	_, err = store.AppendAdd(ctx, ws, token, "r2", KindFollowUp, nil, StorageInline)
	require.NoError(t, err)

	// Duplicate recordID within the same workspace is rejected.
	_, err = store.AppendAdd(ctx, ws, token, "dup-1", KindFollowUp, map[string]any{"v": 1.0}, StorageInline)
	require.NoError(t, err)
	_, err = store.AppendAdd(ctx, ws, token, "dup-1", KindFollowUp, map[string]any{"v": 2.0}, StorageInline)
	require.ErrorIs(t, err, ErrRecordAlreadyExists)

	// Updating a nonexistent record is rejected.
	_, err = store.AppendUpdate(ctx, ws, token, "nonexistent", map[string]any{"v": 1.0}, StorageInline)
	require.ErrorIs(t, err, ErrRecordNotFound)

	// A RecordKind outside the fixed §1.1 enum is rejected.
	_, err = store.AppendAdd(ctx, ws, token, "bad-kind", RecordKind("not-a-real-kind"), map[string]any{"v": 1.0}, StorageInline)
	require.ErrorIs(t, err, ErrInvalidRecordKind)
}

// TestEdgeCases_CanonicalHashHandlesUnicodeAndNesting exercises
// canonicalHash/canonicalJSONBytes against astral-plane characters (which
// must be escaped as UTF-16 surrogate pairs), HTML-sensitive characters
// (which must NOT be HTML-escaped, matching Python's json.dumps), and
// arbitrarily nested composite structures.
func TestEdgeCases_CanonicalHashHandlesUnicodeAndNesting(t *testing.T) {
	body := map[string]any{"emoji": "\U0001F600", "text": "héllo<script>&\"quote\""}
	h, err := canonicalHash(body)
	require.NoError(t, err)
	require.Len(t, h, 64)

	encoded, err := canonicalJSONBytes(body)
	require.NoError(t, err)
	require.Equal(t, `{"emoji":"\ud83d\ude00","text":"h\u00e9llo<script>&\"quote\""}`, string(encoded))

	nested := map[string]any{"a": []any{1.0, map[string]any{"b": []any{"x", "y"}}}}
	_, err = canonicalHash(nested)
	require.NoError(t, err)
}

// TestEdgeCases_NilBodyPersistsAndReplaysAsEmptyObject confirms a nil body
// round-trips through current_state, snapshotting, and replay-verification
// as an empty JSON object without tripping hash verification.
func TestEdgeCases_NilBodyPersistsAndReplaysAsEmptyObject(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "qa-ws2"
	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)

	_, err = store.AppendAdd(ctx, ws, token, "r-nilbody", KindFollowUp, nil, StorageInline)
	require.NoError(t, err)

	rec, found, err := store.GetRecord(ctx, ws, "r-nilbody")
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, rec.Body)

	snap, err := store.CaptureSnapshot(ctx, ws, TriggerPreRollup, token)
	require.NoError(t, err)

	replayed, err := store.Replay(ctx, ws, snap.SnapshotID, ReplayOptions{})
	require.NoError(t, err)
	require.Contains(t, replayed, "r-nilbody")
	require.Empty(t, replayed["r-nilbody"])
}
