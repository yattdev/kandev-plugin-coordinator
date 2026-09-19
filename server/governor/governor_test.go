package governor

import (
	"context"
	"github.com/stretchr/testify/require"
	"kandev-plugin-coordinator/server/durablestate"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) Store {
	t.Helper()
	d, e := durablestate.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, e)
	require.NoError(t, d.Migrate(context.Background()))
	t.Cleanup(func() { d.Close() })
	return Store{Durable: d, Now: func() time.Time { return time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC) }, Config: Config{Watchdog: 3 * time.Hour}}
}
func observation(id string, complete bool) Observation {
	return Observation{SchemaVersion: SchemaVersion, WorkspaceID: "w", ObservedAt: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC), EventID: id, Provenance: "normalized_snapshot", Complete: complete, Tasks: []Task{{ID: "t", Lane: "work", State: "active"}}}
}
func TestObserveIsAdvisoryAndReplaySafe(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	r, e := s.Observe(ctx, 0, observation("e1", false))
	require.NoError(t, e)
	require.Equal(t, TierAstra, r.Decision)
	require.Equal(t, "unavailable: normalized snapshot supplied by caller", r.LiveBoardCollection)
	r, e = s.Observe(ctx, 0, observation("e1", false))
	require.NoError(t, e)
	require.True(t, r.Duplicate)
	m, e := s.Durable.ListMutations(ctx, "w")
	require.NoError(t, e)
	require.Len(t, m, 1)
}
func TestWatchdogAndContract(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, e := s.Observe(ctx, 0, observation("e1", true))
	require.NoError(t, e)
	o := observation("e2", true)
	o.ObservedAt = o.ObservedAt.Add(4 * time.Hour)
	o.Tasks[0].BlockerReason = "waiting on test"
	r, e := s.Observe(ctx, 0, o)
	require.NoError(t, e)
	require.Equal(t, TierAstra, r.Decision)
	require.ErrorIs(t, ValidateContract(Contract{Version: 1, WorkspaceID: "w", TaskID: "t", Head: "h", Generation: "g", ExpiresAt: time.Now().Add(-time.Minute)}, "w", "t", "h", "g", time.Now()), ErrStaleContract)
}

func TestContractCompareAndSwapSurvivesStoreReads(t *testing.T) {
	s := testStore(t)
	contract := Contract{WorkspaceID: "w", TaskID: "t", Goal: "finish", NextAction: "test", Head: "abc", Generation: "g", ExpiresAt: time.Now().Add(time.Hour), AllowedActions: []string{"test"}, ProhibitedActions: []string{"dispatch"}}
	stored, err := s.PutContract(context.Background(), 0, 0, contract)
	require.NoError(t, err)
	require.Equal(t, 1, stored.Version)
	_, err = s.PutContract(context.Background(), 0, 0, contract)
	require.ErrorIs(t, err, ErrStaleContract)
	require.NoError(t, ValidateContract(stored, "w", "t", "abc", "g", time.Now()))
}
