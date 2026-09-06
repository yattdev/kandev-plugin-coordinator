package durablestate

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDurability_StateSurvivesProcessRestartAgainstFileBackedDB proves the
// engine's durability guarantee against a real file on disk, not just an
// in-memory connection kept alive for the lifetime of one *Store: it opens
// a fresh *Store bound to the same SQLite file after the first one is
// closed (simulating a process restart), and confirms current-state,
// mutation log, snapshots, and compaction receipts are all still present
// and correct.
func TestDurability_StateSurvivesProcessRestartAgainstFileBackedDB(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "durable.sqlite3")
	ws := "w1"

	func() {
		store, err := Open(dbPath)
		require.NoError(t, err)
		defer store.Close()
		require.NoError(t, store.Migrate(ctx))

		token, err := store.AcquireLease(ctx, ws, "leader-a")
		require.NoError(t, err)
		_, err = store.AppendAdd(ctx, ws, token, "ra", KindFollowUp, map[string]any{"n": float64(1)}, StorageInline)
		require.NoError(t, err)
		_, err = store.AppendAdd(ctx, ws, token, "rb", KindFollowUp, map[string]any{"n": float64(2)}, StorageInline)
		require.NoError(t, err)
		_, err = store.CaptureSnapshot(ctx, ws, TriggerScheduledCadence, token)
		require.NoError(t, err)
		_, err = store.Compact(ctx, ws, token, "", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
		require.NoError(t, err)
	}()

	// Reopen against the same file: a brand new *sql.DB / *Store, with no
	// shared in-memory state whatsoever, standing in for a fresh process.
	restarted, err := Open(dbPath)
	require.NoError(t, err)
	defer restarted.Close()

	_, found, err := restarted.GetRecord(ctx, ws, "ra")
	require.NoError(t, err)
	require.False(t, found, "ra was rolled up before restart and must stay absent from current_state")

	rb, found, err := restarted.GetRecord(ctx, ws, "rb")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, float64(2), rb.Body["n"])

	mutations, err := restarted.ListMutations(ctx, ws)
	require.NoError(t, err)
	require.Len(t, mutations, 3) // add ra, add rb, remove ra (compaction)

	snapshots, err := restarted.ListSnapshots(ctx, ws)
	require.NoError(t, err)
	require.Len(t, snapshots, 2) // pre-rollup snapshot + explicit scheduled snapshot

	receipts, err := restarted.ListCompactionReceipts(ctx, ws)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.Equal(t, "committed", receipts[0].Phase)

	token, err := restarted.CurrentFencingToken(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, int64(1), token)
}
