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
	_, err = applyFixtureGrant(context.Background(), p, store, &grant, origin, "op", "competing-task", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
	grant.Revoked = true
	_, err = applyFixtureGrant(context.Background(), p, store, &grant, origin, "op", "blocked-target", fixtureAction)
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
	_, err = applyFixtureGrant(context.Background(), p, store, &grant, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	replay, err := applyFixtureGrant(context.Background(), p, store, &grant, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	require.True(t, replay.Replay)
}
