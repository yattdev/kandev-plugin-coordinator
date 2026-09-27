package coordinator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"kandev-plugin-coordinator/server/durablestate"
	"kandev-plugin-coordinator/server/governor"
)

func fixtureStore(t *testing.T) governor.Store {
	t.Helper()
	d, err := durablestate.Open(filepath.Join(t.TempDir(), "fixture.db"))
	require.NoError(t, err)
	require.NoError(t, d.Migrate(context.Background()))
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	return governor.Store{Durable: d, Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }}
}

func TestFixturePOCUsesCoordinatorActionAndGovernorReceipt(t *testing.T) {
	report, err := runFixturePOC(context.Background(), fixtureStore(t))
	require.NoError(t, err)
	require.Equal(t, governor.TierSol, report.Decision.Decision)
	require.Equal(t, "blocked-target", report.Grant.TargetID)
	require.True(t, report.Grant.Used)
	require.Equal(t, "Ready", report.Readback.State)
}

func TestFixtureGrantDeniesCompetingTargetAndRevocation(t *testing.T) {
	store := fixtureStore(t)
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err := fixtureActionCall(context.Background(), p, origin)
	require.NoError(t, err)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction}
	board := newFixtureBoard()
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "competing-task", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
	grant.Revoked = true
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "blocked-target", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
}

func TestFixtureGrantReplayHasNoDuplicateEffect(t *testing.T) {
	store := fixtureStore(t)
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err := fixtureActionCall(context.Background(), p, origin)
	require.NoError(t, err)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction}
	board := newFixtureBoard()
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	replay, err := applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	require.True(t, replay.Replay)
}

func TestFixtureGrantReplaySurvivesStoreReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	d, err := durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	store := governor.Store{Durable: d, Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }}
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err = fixtureActionCall(ctx, p, origin)
	require.NoError(t, err)
	board := newFixtureBoard()
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction}
	_, err = applyFixtureGrant(ctx, p, store, board, &grant, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	require.Equal(t, 1, board.mutations)
	require.NoError(t, d.Close())
	d, err = durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	defer d.Close()
	store.Durable = d
	board, err = loadFixtureBoard(ctx, d)
	require.NoError(t, err)
	require.Equal(t, "Ready", board.tasks["blocked-target"].State)
	replay, err := applyFixtureGrant(ctx, New(), store, board, &FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction}, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	require.True(t, replay.Replay)
	require.Equal(t, 1, board.mutations)
}
