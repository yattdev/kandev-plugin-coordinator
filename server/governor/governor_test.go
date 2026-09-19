package governor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/require"
	"kandev-plugin-coordinator/server/durablestate"
	"path/filepath"
	"sync"
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

func TestConcurrentTransformsPreserveObservationsContractsAndReviewBaseline(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, b := observation("a", true), observation("b", true)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, o := range []Observation{a, b} {
		wg.Add(1)
		go func(o Observation) { defer wg.Done(); _, err := s.Observe(ctx, 0, o); errs <- err }(o)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		_ = <-errs
	}
	c := Contract{StrategyVersion: 1, PlanVersion: 1, WorkspaceID: "w", TaskID: "other", Goal: "finish", NextAction: "test", ExecutorTier: TierTerra, Head: "h", Generation: "g", ExpiresAt: time.Now().Add(time.Hour), AllowedActions: []string{"test"}, CompletionConditions: []string{"pass"}}
	wg.Add(2)
	go func() { defer wg.Done(); _, err := s.PutContract(ctx, 0, 0, c); errs <- err }()
	go func() {
		defer wg.Done()
		errs <- s.AcknowledgeStrategy(ctx, 0, "w", StrategyReceipt{EventID: "b", RequestID: "r", IncidentID: "i", EvidenceID: b.EvidenceID, Model: TierAstra, Outcome: "completed", Accepted: true, StrategyVersion: 1, PlanVersion: 1, ExpectedEffect: "effect", EffectDueAt: b.ObservedAt.Add(time.Hour), CompletedAt: b.ObservedAt.Add(time.Second)})
	}()
	wg.Wait()
	var success bool
	for i := 0; i < 2; i++ {
		if <-errs == nil {
			success = true
		}
	}
	require.True(t, success)
	r, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
	require.NoError(t, err)
	require.True(t, ok)
	raw, _ := json.Marshal(r.Body)
	var st state
	require.NoError(t, json.Unmarshal(raw, &st))
	require.Len(t, st.Events, 2)
	require.Contains(t, st.Contracts, "other")
	if !st.ReviewBaseline.ObservedAt.IsZero() {
		require.Equal(t, st.Last.EventID, st.ReviewBaseline.EventID)
	}
}

func TestConcurrentSameContractVersionHasOneWinner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c := Contract{StrategyVersion: 1, PlanVersion: 1, WorkspaceID: "w", TaskID: "t", Goal: "finish", NextAction: "test", ExecutorTier: TierTerra, Head: "h", Generation: "g", ExpiresAt: time.Now().Add(time.Hour), AllowedActions: []string{"test"}, CompletionConditions: []string{"pass"}}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.PutContract(ctx, 0, 0, c); results <- err }()
	}
	wg.Wait()
	close(results)
	ok, stale := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrStaleContract) {
			stale++
		}
	}
	require.Equal(t, 1, ok)
	require.Equal(t, 1, stale)
}

func TestOutcomeAgeRoutesAstraDespiteActivityAndDigestNeverDropsLastAttention(t *testing.T) {
	s := testStore(t)
	o := observation("one", true)
	due := o.ObservedAt.Add(-time.Minute)
	o.Tasks = []Task{{ID: "a", State: "active", EvidenceDueAt: due, ActivityAt: o.ObservedAt}, {ID: "b", State: "active", EvidenceDueAt: due, ActivityAt: o.ObservedAt}}
	_, err := s.Observe(context.Background(), 0, o)
	require.NoError(t, err)
	o.EventID = "two"
	o.ObservedAt = o.ObservedAt.Add(time.Minute)
	o.Tasks[0].ActivityAt = o.ObservedAt
	r, err := s.Observe(context.Background(), 0, o)
	require.NoError(t, err)
	require.Equal(t, TierAstra, r.Decision)
	require.Contains(t, r.Reasons, "overdue_cohort")
	tiny := observation("tiny", true)
	tiny.WorkspaceID = "tiny"
	tiny.Tasks[0].BlockerReason = "x"
	_, err = Store{Durable: s.Durable, Now: s.Now, Config: Config{MaxDigestBytes: 1}}.Observe(context.Background(), 0, tiny)
	require.ErrorIs(t, err, ErrDigestTooSmall)
}

func TestReplayConflictDistinctContractsAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reopen.db")
	d, err := durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	s := Store{Durable: d, Now: func() time.Time { return time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC) }}
	o := observation("replay", true)
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	o.Tasks[0].Owner = "changed"
	_, err = s.Observe(ctx, 0, o)
	require.Error(t, err)
	makeContract := func(id string) Contract {
		return Contract{StrategyVersion: 1, PlanVersion: 1, WorkspaceID: "w", TaskID: id, Goal: "g", NextAction: "a", ExecutorTier: TierLuna, Head: "h", Generation: "g", ExpiresAt: time.Now().Add(time.Hour), AllowedActions: []string{"a"}, CompletionConditions: []string{"done"}}
	}
	_, err = s.PutContract(ctx, 0, 0, makeContract("one"))
	require.NoError(t, err)
	_, err = s.PutContract(ctx, 0, 0, makeContract("two"))
	require.NoError(t, err)
	require.NoError(t, s.Durable.Close())
	d, err = durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	defer d.Close()
	reopened := Store{Durable: d, Now: s.Now}
	_, err = reopened.ValidateCurrentContract(ctx, "w", "two", "h", "g", 1, 1, time.Now())
	require.NoError(t, err)
}

func TestDigestBoundsCountEveryOmission(t *testing.T) {
	d := Digest{SchemaVersion: SchemaVersion, WorkspaceID: "w", Attention: []Attention{{TaskID: "1", Reason: "a", Tier: TierAstra}, {TaskID: "2", Reason: "b", Tier: TierSol}, {TaskID: "3", Reason: "c", Tier: TierTerra}}}
	full, _ := json.Marshal(d)
	require.NoError(t, boundDigest(&d, len(full)-20))
	require.GreaterOrEqual(t, d.Omitted, 1)
}

func TestReceiptRejectsUnverifiedReviewWithoutWatermark(t *testing.T) {
	s := testStore(t)
	o := observation("review", true)
	_, err := s.Observe(context.Background(), 0, o)
	require.NoError(t, err)
	err = s.AcknowledgeStrategy(context.Background(), 0, "w", StrategyReceipt{EventID: o.EventID, RequestID: "r", IncidentID: "i", EvidenceID: o.EvidenceID, Model: TierSol, Outcome: "completed", Accepted: true, StrategyVersion: 1, PlanVersion: 1, ExpectedEffect: "x", EffectDueAt: o.ObservedAt.Add(time.Hour), CompletedAt: o.ObservedAt.Add(time.Minute)})
	require.Error(t, err)
	r, ok, err := s.Durable.GetRecord(context.Background(), "w", stateRecordID)
	require.NoError(t, err)
	require.True(t, ok)
	raw, _ := json.Marshal(r.Body)
	var st state
	require.NoError(t, json.Unmarshal(raw, &st))
	require.True(t, st.StrategyAt.IsZero())
}

func TestRoutingHonorsAllTierPrecedence(t *testing.T) {
	for _, tc := range []struct {
		tier Tier
		want Tier
	}{{TierLuna, TierLuna}, {TierTerra, TierTerra}} {
		s := testStore(t)
		o := observation("tier-"+string(tc.tier), true)
		o.Tasks[0].ExecutorTier = tc.tier
		r, err := s.Observe(context.Background(), 0, o)
		require.NoError(t, err)
		require.Equal(t, tc.want, r.Decision)
	}
}

func TestMissingOrRecurringSolEffectEscalatesAstraAndReplayIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	o := observation("sol", true)
	o.ObservedAt = time.Date(2026, 9, 19, 16, 0, 0, 0, time.UTC)
	_, err := s.Observe(ctx, 0, o)
	require.NoError(t, err)
	r := SolRecovery{IncidentID: "i", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "r", ProposedAction: "reproduce", ExpectedEffect: "test passes", ActualModel: TierSol, Status: "accepted", Accepted: true, StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, EffectDueAt: o.ObservedAt.Add(time.Minute)}
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	o.EventID = "sol-next"
	o.EvidenceID = "e-next"
	o.ObservedAt = o.ObservedAt.Add(2 * time.Minute)
	result, err := s.Observe(ctx, 0, o)
	require.NoError(t, err)
	require.Equal(t, TierAstra, result.Decision)
	require.Contains(t, result.Reasons, "ineffective_sol_recovery")
}
func observation(id string, complete bool) Observation {
	return Observation{SchemaVersion: SchemaVersion, WorkspaceID: "w", ObservedAt: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC), EventID: id, Provenance: "normalized_snapshot", Complete: complete, StrategyVersion: 1, PlanVersion: 1, EvidenceID: "evidence-" + id, Tasks: []Task{{ID: "t", Lane: "work", State: "active"}}}
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
	contract := Contract{StrategyVersion: 1, PlanVersion: 1, WorkspaceID: "w", TaskID: "t", Goal: "finish", NextAction: "test", ExecutorTier: TierTerra, Head: "abc", Generation: "g", ExpiresAt: time.Now().Add(time.Hour), AllowedActions: []string{"test"}, ProhibitedActions: []string{"dispatch"}, CompletionConditions: []string{"test passes"}}
	stored, err := s.PutContract(context.Background(), 0, 0, contract)
	require.NoError(t, err)
	require.Equal(t, 1, stored.Version)
	_, err = s.PutContract(context.Background(), 0, 0, contract)
	require.ErrorIs(t, err, ErrStaleContract)
	require.NoError(t, ValidateContract(stored, "w", "t", "abc", "g", time.Now()))
	_, err = s.ValidateCurrentContract(context.Background(), "w", "t", "abc", "g", 1, 2, time.Now())
	require.ErrorIs(t, err, ErrStaleContract)
	current, err := s.ValidateCurrentContract(context.Background(), "w", "t", "abc", "g", 1, 1, time.Now())
	require.NoError(t, err)
	require.Equal(t, stored.Version, current.Version)
}
