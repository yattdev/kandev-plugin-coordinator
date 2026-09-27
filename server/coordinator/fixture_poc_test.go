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
	require.Len(t, report.Before, 3)
	require.Len(t, report.After, 3)
	require.Equal(t, "blocked-target", report.Before[0].ID)
	require.Equal(t, "Blocked", report.Before[0].State)
	require.Equal(t, "blocked-target", report.After[0].ID)
	require.Equal(t, "Ready", report.After[0].State)
	require.Equal(t, "done-dependency", report.Before[1].ID)
	require.Equal(t, "Done", report.Before[1].State)
	require.Equal(t, "in-progress", report.Before[2].ID)
	require.Equal(t, "InProgress", report.Before[2].State)
	require.Equal(t, []string{"done-dependency"}, report.Before[0].Dependencies)
	require.Len(t, report.Denials, 3)
	require.Equal(t, "stale_evidence", report.Denials[2].Reason)
}

func TestFixtureGrantDeniesCompetingTargetAndRevocation(t *testing.T) {
	store := fixtureStore(t)
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err := fixtureActionCall(context.Background(), p, origin)
	require.NoError(t, err)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	board := newFixtureBoard()
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "competing-task", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
	require.Equal(t, 0, board.mutations)
	current, err := board.reader.Get(context.Background(), "blocked-target")
	require.NoError(t, err)
	require.Equal(t, "Blocked", current.State)
	grant.Revoked = true
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "blocked-target", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
	require.Equal(t, 0, board.mutations)
}

func TestFixtureGrantReplayHasNoDuplicateEffect(t *testing.T) {
	store := fixtureStore(t)
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err := fixtureActionCall(context.Background(), p, origin)
	require.NoError(t, err)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	board := newFixtureBoard()
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	updated, err := board.reader.Get(context.Background(), "blocked-target")
	require.NoError(t, err)
	require.Equal(t, "Ready", updated.State)
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
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
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
	rows, err := board.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "Ready", rows[0].State)
	replay, err := applyFixtureGrant(ctx, New(), store, board, &FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	require.True(t, replay.Replay)
	require.Equal(t, 1, board.mutations)
}

func TestUnverifiedFixtureProjectionReopensUnknown(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "interrupted.db")
	d, err := durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	_, err = d.AppendAdd(ctx, "fixture-workspace", 0, "fixture-operation-1", durablestate.KindDoneReceipt, map[string]any{"state": "Ready", "receipt": "r", "target": "blocked-target", "action": fixtureAction, "grant": "g", "operation": "fixture-operation-1", "verified": false}, durablestate.StorageInline)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	d, err = durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	defer d.Close()
	_, err = loadFixtureBoard(ctx, d)
	require.ErrorIs(t, err, ErrFixtureOutcomeUnknown)
}
