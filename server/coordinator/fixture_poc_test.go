package coordinator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
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

func fixtureTestBoard(t *testing.T, durable *durablestate.Store) *fixtureBoard {
	t.Helper()
	board, err := newFixtureBoard(context.Background(), durable)
	require.NoError(t, err)
	return board
}

func TestFixturePOCUsesCoordinatorActionAndGovernorReceipt(t *testing.T) {
	report, err := runFixturePOC(context.Background(), fixtureStore(t))
	require.NoError(t, err)
	require.Equal(t, governor.TierSol, report.Decision.Decision)
	require.Equal(t, fixtureAction, report.SelectedAction)
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
	require.Len(t, report.Denials, 4)
	require.Equal(t, "non_exact_action", report.Denials[2].Reason)
	require.Equal(t, "stale_evidence", report.Denials[3].Reason)
}

func TestFixtureGrantDeniesCompetingTargetAndRevocation(t *testing.T) {
	store := fixtureStore(t)
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err := fixtureActionCall(context.Background(), p, origin)
	require.NoError(t, err)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	board := fixtureTestBoard(t, store.Durable)
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "competing-task", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
	require.Equal(t, 0, board.mutations)
	current, err := board.taskReader.Get(context.Background(), "blocked-target")
	require.NoError(t, err)
	require.Equal(t, "Blocked", current.State)
	grant.Revoked = true
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "blocked-target", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
	require.Equal(t, 0, board.mutations)
	wrongAction := FixtureGrant{ID: "g-action", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	_, err = applyFixtureGrant(context.Background(), p, store, board, &wrongAction, origin, "op-action", "blocked-target", "fixture.other")
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
	require.Equal(t, 0, board.mutations)
}

func TestFixtureGrantDeniesSupersededOriginBeforeIntentOrMutation(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "shared-evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err := fixtureActionCall(ctx, p, origin)
	require.NoError(t, err)
	superseding := origin
	superseding.EventID = "origin-superseded"
	superseding.ObservedAt = origin.ObservedAt.Add(time.Minute)
	// The evidence token intentionally remains unchanged: the grant must still
	// be rejected because its complete origin observation is no longer current.
	_, err = fixtureActionCall(ctx, p, superseding)
	require.NoError(t, err)
	board := fixtureTestBoard(t, store.Durable)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	_, err = applyFixtureGrant(ctx, p, store, board, &grant, origin, "superseded-op", "blocked-target", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
	require.Equal(t, 0, board.mutations)
	task, err := board.taskReader.Get(ctx, "blocked-target")
	require.NoError(t, err)
	require.Equal(t, "Blocked", task.State)
	_, found, err := store.Durable.GetRecord(ctx, origin.WorkspaceID, "superseded-op")
	require.NoError(t, err)
	require.False(t, found)
}

func TestFixtureBoardUsesHostTasksBoundary(t *testing.T) {
	board := fixtureTestBoard(t, nil)
	require.Same(t, board.taskReader, board.host.Tasks())
	tasks, info, err := board.host.Tasks().List(context.Background(), pluginsdk.TaskFilter{}, pluginsdk.Page{})
	require.NoError(t, err)
	require.True(t, info.HasMore)
	require.Len(t, tasks, 1)
}

func TestFixtureGrantReplayHasNoDuplicateEffect(t *testing.T) {
	store := fixtureStore(t)
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err := fixtureActionCall(context.Background(), p, origin)
	require.NoError(t, err)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	board := fixtureTestBoard(t, store.Durable)
	_, err = applyFixtureGrant(context.Background(), p, store, board, &grant, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	updated, err := board.taskReader.Get(context.Background(), "blocked-target")
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
	board := fixtureTestBoard(t, d)
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
	board, err = loadFixtureBoard(ctx, d, "op")
	require.NoError(t, err)
	rows, err := board.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "Ready", rows[0].State)
	replay, err := applyFixtureGrant(ctx, New(), store, board, &FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	require.True(t, replay.Replay)
	require.Equal(t, 0, board.mutations)
}

func TestUnverifiedFixtureProjectionReopensUnknown(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "interrupted.db")
	d, err := durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	_, err = d.AppendAdd(ctx, "fixture-workspace", 0, "fixture-operation-1", durablestate.KindDoneReceipt, map[string]any{"phase": "effect-applied", "state": "Ready", "receipt": "r", "target": "blocked-target", "action": fixtureAction, "grant": "g", "evidence": "evidence", "operation": "fixture-operation-1", "verified": false}, durablestate.StorageInline)
	require.NoError(t, err)
	require.NoError(t, d.Close())
	d, err = durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	defer d.Close()
	_, err = loadFixtureBoard(ctx, d, "fixture-operation-1")
	require.ErrorIs(t, err, ErrFixtureOutcomeUnknown)
	store := governor.Store{Durable: d, Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }}
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	board := fixtureTestBoard(t, d)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	_, err = applyFixtureGrant(ctx, New(), store, board, &grant, origin, "fixture-operation-1", "blocked-target", fixtureAction)
	require.ErrorIs(t, err, ErrFixtureOutcomeUnknown)
	require.Equal(t, 0, board.mutations)
}

func TestFixtureReaderFailureFailsClosed(t *testing.T) {
	board := fixtureTestBoard(t, nil)
	board.taskReader.listErr = errors.New("list failed")
	_, err := board.Read(context.Background())
	require.ErrorContains(t, err, "list failed")
}

func TestFixtureReaderIncompleteCursorFailsClosed(t *testing.T) {
	board := fixtureTestBoard(t, nil)
	board.taskReader.badCursor = true
	_, err := board.Read(context.Background())
	require.ErrorIs(t, err, ErrFixtureGrantDenied)
}

func TestFixtureGrantReconcilesInterruptionAfterGovernorVerification(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reconcile.db")
	d, err := durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	store := governor.Store{Durable: d, Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }}
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err = fixtureActionCall(ctx, p, origin)
	require.NoError(t, err)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	interrupted := fixtureTestBoard(t, d)
	fault := errors.New("injected interruption")
	_, err = applyFixtureGrantWithHooks(ctx, p, store, interrupted, &grant, origin, "op", "blocked-target", fixtureAction, fixtureApplyHooks{BeforeFinalRecord: func() error { return fault }})
	require.ErrorIs(t, err, fault)
	require.Equal(t, 1, interrupted.mutations)
	require.NoError(t, d.Close())

	d, err = durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	defer d.Close()
	store.Durable = d
	restarted, err := loadFixtureBoard(ctx, d, "op")
	require.NoError(t, err)
	p = New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	replay, err := applyFixtureGrant(ctx, p, store, restarted, &FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	require.True(t, replay.Replay)
	require.Equal(t, 0, restarted.mutations)
	rows, err := restarted.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "Ready", rows[0].State)
	require.Empty(t, rows[0].BlockerReason)
	record, found, err := d.GetRecord(ctx, "fixture-workspace", "op")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, true, record.Body["verified"])
}

func TestFixtureGrantReconcilesInterruptionBeforeOperationRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "before-operation.db")
	d, err := durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	store := governor.Store{Durable: d, Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }}
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("origin", "evidence", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	_, err = fixtureActionCall(ctx, p, origin)
	require.NoError(t, err)
	grant := FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}
	interrupted := fixtureTestBoard(t, d)
	fault := errors.New("injected interruption")
	_, err = applyFixtureGrantWithHooks(ctx, p, store, interrupted, &grant, origin, "op", "blocked-target", fixtureAction, fixtureApplyHooks{BeforeOperationRecord: func() error { return fault }})
	require.ErrorIs(t, err, fault)
	require.Equal(t, 0, interrupted.mutations)
	_, found, err := d.GetRecord(ctx, "fixture-workspace", "op")
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, d.Close())

	d, err = durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	defer d.Close()
	store.Durable = d
	restarted, err := loadFixtureBoard(ctx, d, "op")
	require.NoError(t, err)
	p = New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	readback, err := applyFixtureGrant(ctx, p, store, restarted, &FixtureGrant{ID: "g", TargetID: "blocked-target", Action: fixtureAction, EvidenceID: origin.EvidenceID}, origin, "op", "blocked-target", fixtureAction)
	require.NoError(t, err)
	require.False(t, readback.Replay)
	require.Equal(t, 1, restarted.mutations)
}
