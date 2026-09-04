package durablestate

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConcurrency_ParallelAppendsSerializeWithoutCorruption exercises
// real goroutine concurrency against withWriteTx's BEGIN IMMEDIATE lock:
// many goroutines racing to add distinct records under the same fencing
// token must all succeed with no lost/duplicated mutation_ids.
func TestConcurrency_ParallelAppendsSerializeWithoutCorruption(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)

	const n = 25
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.AppendAdd(ctx, ws, token, recIDForN(i), KindDirtyTask, map[string]any{"i": float64(i)}, StorageInline)
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	records, err := store.ListRecords(ctx, ws, KindDirtyTask)
	require.NoError(t, err)
	require.Len(t, records, n)

	mutations, err := store.ListMutations(ctx, ws)
	require.NoError(t, err)
	require.Len(t, mutations, n)
	seen := make(map[int64]bool, n)
	for _, m := range mutations {
		require.False(t, seen[m.MutationID], "mutation_id %d must be unique", m.MutationID)
		seen[m.MutationID] = true
	}
}

// TestConcurrency_ConcurrentCompactionAttemptsOneWinsOneFencedOut runs the
// stale-leader-vs-new-leader race from TestCompaction_ConcurrentCompaction
// through actual goroutines rather than sequential calls, confirming the
// write lock plus fencing check together produce exactly one winner.
func TestConcurrency_ConcurrentCompactionAttemptsOneWinsOneFencedOut(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	setupWorkspaceWithRecords(t, store, ws, 4)

	tokenA, err := store.AcquireLease(ctx, ws, "leader-a")
	require.NoError(t, err)
	tokenB, err := store.AcquireLease(ctx, ws, "leader-b")
	require.NoError(t, err)

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = store.Compact(ctx, ws, tokenA, "compaction-a", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	}()
	go func() {
		defer wg.Done()
		_, errB = store.Compact(ctx, ws, tokenB, "compaction-b", []RolledRecordInput{{RecordID: "rb", ResolvedAt: nowUTC()}})
	}()
	wg.Wait()

	// tokenA is stale relative to tokenB regardless of goroutine
	// scheduling order, so it must always be the one rejected.
	require.Error(t, errA)
	require.NoError(t, errB)
}

func recIDForN(i int) string {
	return "r" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

func TestHealth_ReflectsCountersAndStuckCompactions(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	ws := "w1"
	token := setupWorkspaceWithRecords(t, store, ws, 2)

	health, err := store.GetHealth(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, int64(1), health.LeaseAcquisitions)
	require.Equal(t, token, health.CurrentFencingToken)
	require.Equal(t, 0, health.StuckCompactions)

	_, err = store.archiveCompaction(ctx, ws, token, "c-1", []RolledRecordInput{{RecordID: "ra", ResolvedAt: nowUTC()}})
	require.NoError(t, err)

	health, err = store.GetHealth(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, 1, health.StuckCompactions)
	require.Equal(t, 1, health.ArchiveRecordCount)

	resumed, err := store.ResumeCompactions(ctx, ws, token)
	require.NoError(t, err)
	require.Len(t, resumed, 1)

	health, err = store.GetHealth(ctx, ws)
	require.NoError(t, err)
	require.Equal(t, 0, health.StuckCompactions)
	require.Equal(t, int64(1), health.CompactionRecoveries)
}
