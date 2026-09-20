package governor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"kandev-plugin-coordinator/server/durablestate"
)

func TestRecoveryLifecycleSurvivesReopenAndVerifiesLaterEffect(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery.db")
	d, err := durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	now := time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC)
	s := Store{Durable: d, Now: func() time.Time { return now }}
	o := observation("origin", true)
	o.ObservedAt = now.Add(-2 * time.Hour)
	o.Tasks = []Task{{ID: "t", Head: "h", PlanVersion: 1, State: "active"}}
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	r := SolRecovery{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "request", ReceiptID: "requested", ActualModel: TierSol, ActualModelReceipt: "actual", Status: "requested", StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, AffectedTaskIDs: []string{"t"}}
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	r.Status = "started"
	r.ReceiptID = "started"
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	r.Status = "completed"
	r.ReceiptID = "completed"
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	r.Status = "decision_accepted"
	r.ReceiptID = "accepted"
	r.Accepted = true
	r.ProposedAction = "run"
	r.ExpectedEffect = "pass"
	r.EffectDueAt = now.Add(time.Hour)
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	require.NoError(t, d.Close())
	d, err = durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	defer d.Close()
	s.Durable = d
	o.EventID = "effect"
	o.EvidenceID = "effect-evidence"
	o.ObservedAt = now.Add(-time.Minute)
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	err = s.VerifySolRecoveryEffectReceipt(ctx, 0, "w", RecoveryEffectReceipt{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, TaskID: "t", Head: "h", PlanVersion: 1, StrategyVersion: 1, Verifier: "test", Milestone: "pass", ObservedAt: o.ObservedAt})
	require.NoError(t, err)
}

func TestRecoveryRecurrenceRequiresLatestMatchingCompleteEvidence(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	o := observation("origin", true)
	o.Tasks = []Task{{ID: "t", Head: "h", PlanVersion: 1, State: "active"}}
	_, err := s.Observe(ctx, 0, o)
	require.NoError(t, err)
	r := SolRecovery{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "request", ReceiptID: "accepted", ProposedAction: "run", ExpectedEffect: "pass", ActualModel: TierSol, ActualModelReceipt: "actual", Status: "decision_accepted", Accepted: true, StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, EffectDueAt: o.ObservedAt.Add(time.Hour), AffectedTaskIDs: []string{"t"}}
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	o.EventID = "effect"
	o.EvidenceID = "effect-e"
	o.ObservedAt = o.ObservedAt.Add(time.Minute)
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	require.NoError(t, s.VerifySolRecoveryEffectReceipt(ctx, 0, "w", RecoveryEffectReceipt{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, TaskID: "t", Head: "h", PlanVersion: 1, StrategyVersion: 1, Verifier: "v", Milestone: "m", ObservedAt: o.ObservedAt}))
	err = s.RecordSolRecurrenceReceipt(ctx, 0, "w", RecoveryRecurrenceReceipt{IncidentID: "incident", EventID: "wrong", EvidenceID: o.EvidenceID, StrategyVersion: 1, PlanVersion: 1, RecurrenceID: "r", ReceiptID: "wrong", ObservedAt: o.ObservedAt})
	require.ErrorIs(t, err, ErrStaleContract)
	err = s.RecordSolRecurrenceReceipt(ctx, 0, "w", RecoveryRecurrenceReceipt{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, StrategyVersion: 2, PlanVersion: 1, RecurrenceID: "r", ReceiptID: "wrong-generation", ObservedAt: o.ObservedAt})
	require.ErrorIs(t, err, ErrStaleContract)
	err = s.RecordSolRecurrenceReceipt(ctx, 0, "w", RecoveryRecurrenceReceipt{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, StrategyVersion: 1, PlanVersion: 1, RecurrenceID: "r", ReceiptID: "same", ObservedAt: o.ObservedAt})
	require.ErrorIs(t, err, ErrStaleContract)
	o.EventID = "recurrence"
	o.EvidenceID = "recurrence-e"
	o.ObservedAt = o.ObservedAt.Add(time.Minute)
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	err = s.RecordSolRecurrenceReceipt(ctx, 0, "w", RecoveryRecurrenceReceipt{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, StrategyVersion: 1, PlanVersion: 1, RecurrenceID: "r", ReceiptID: "recurrence", ObservedAt: o.ObservedAt})
	require.NoError(t, err)
	o.EventID = "after"
	o.EvidenceID = "after-e"
	o.ObservedAt = o.ObservedAt.Add(time.Minute)
	result, err := s.Observe(ctx, 0, o)
	require.NoError(t, err)
	require.Equal(t, TierAstra, result.Decision)
	require.Contains(t, result.Reasons, "ineffective_sol_recovery")
}

func TestRecoveryRecurrenceReceiptReplaySurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "replay.db")
	d, err := durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	now := time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC)
	s := Store{Durable: d, Now: func() time.Time { return now }}
	o := observation("origin", true)
	o.ObservedAt = now.Add(-2 * time.Hour)
	o.Tasks = []Task{{ID: "t", Head: "h", PlanVersion: 1, State: "active"}}
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	r := SolRecovery{IncidentID: "i", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "q", ReceiptID: "accepted", ProposedAction: "run", ExpectedEffect: "pass", ActualModel: TierSol, ActualModelReceipt: "actual", Status: "decision_accepted", Accepted: true, StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, EffectDueAt: now.Add(time.Hour), AffectedTaskIDs: []string{"t"}}
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	o.EventID = "effect"
	o.EvidenceID = "effect-e"
	o.ObservedAt = now.Add(-time.Hour)
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	require.NoError(t, s.VerifySolRecoveryEffectReceipt(ctx, 0, "w", RecoveryEffectReceipt{IncidentID: "i", EventID: o.EventID, EvidenceID: o.EvidenceID, TaskID: "t", Head: "h", PlanVersion: 1, StrategyVersion: 1, Verifier: "v", Milestone: "m", ObservedAt: o.ObservedAt}))
	o.EventID = "recurrence"
	o.EvidenceID = "rec-e"
	o.ObservedAt = now.Add(-30 * time.Minute)
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	receipt := RecoveryRecurrenceReceipt{IncidentID: "i", EventID: o.EventID, EvidenceID: o.EvidenceID, RecurrenceID: "r", ReceiptID: "rec-r", StrategyVersion: 1, PlanVersion: 1, ObservedAt: o.ObservedAt}
	require.NoError(t, s.RecordSolRecurrenceReceipt(ctx, 0, "w", receipt))
	require.NoError(t, d.Close())
	d, err = durablestate.Open(path)
	require.NoError(t, err)
	require.NoError(t, d.Migrate(ctx))
	defer d.Close()
	s.Durable = d
	o.EventID = "newer"
	o.EvidenceID = "new-e"
	o.ObservedAt = now.Add(-time.Minute)
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	beforeRec, ok, err := d.GetRecord(ctx, "w", stateRecordID)
	require.NoError(t, err)
	require.True(t, ok)
	before, _ := json.Marshal(beforeRec.Body)
	require.NoError(t, s.RecordSolRecurrenceReceipt(ctx, 0, "w", receipt))
	afterReplay, ok, err := d.GetRecord(ctx, "w", stateRecordID)
	require.NoError(t, err)
	require.True(t, ok)
	after, _ := json.Marshal(afterReplay.Body)
	require.JSONEq(t, string(before), string(after))
	altered := receipt
	altered.RecurrenceID = "changed"
	require.ErrorIs(t, s.RecordSolRecurrenceReceipt(ctx, 0, "w", altered), ErrStaleContract)
	afterConflict, ok, err := d.GetRecord(ctx, "w", stateRecordID)
	require.NoError(t, err)
	require.True(t, ok)
	conflict, _ := json.Marshal(afterConflict.Body)
	require.JSONEq(t, string(before), string(conflict))
	o.EventID = "after"
	o.EvidenceID = "after-e"
	o.ObservedAt = now
	result, err := s.Observe(ctx, 0, o)
	require.NoError(t, err)
	require.Equal(t, TierAstra, result.Decision)
	require.Contains(t, result.Reasons, "ineffective_sol_recovery")
}

func TestRecoveryTransitionRejectsAffectedTaskMutation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	o := observation("origin", true)
	o.Tasks = []Task{{ID: "t", Head: "h", PlanVersion: 1, State: "active"}, {ID: "other", Head: "h2", PlanVersion: 1, State: "active"}}
	_, err := s.Observe(ctx, 0, o)
	require.NoError(t, err)
	r := SolRecovery{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "request", ReceiptID: "requested", ActualModel: TierSol, ActualModelReceipt: "actual", Status: "requested", StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, AffectedTaskIDs: []string{"t"}}
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	r.Status, r.ReceiptID, r.AffectedTaskIDs = "started", "started", []string{"other"}
	require.ErrorIs(t, s.RecordSolRecovery(ctx, 0, "w", r), ErrStaleContract)
}

func TestRejectedRecoveryTransitionsLeaveDurableStateUntouched(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		prepare func(*Store, *Observation, *SolRecovery) error
	}{
		{
			name: "affected task identity mutation",
			prepare: func(_ *Store, _ *Observation, r *SolRecovery) error {
				r.AffectedTaskIDs = []string{"other"}
				return nil
			},
		},
		{
			name: "future completion",
			prepare: func(s *Store, _ *Observation, r *SolRecovery) error {
				r.CompletedAt = s.Now().Add(time.Minute)
				return nil
			},
		},
		{
			name: "stale observation",
			prepare: func(s *Store, o *Observation, r *SolRecovery) error {
				next := *o
				next.EventID, next.EvidenceID = "newer", "newer-evidence"
				next.ObservedAt = o.ObservedAt.Add(time.Minute)
				_, err := s.Observe(ctx, 0, next)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStore(t)
			o := observation("origin", true)
			o.Tasks = []Task{{ID: "t", Head: "h", PlanVersion: 1, State: "active"}, {ID: "other", Head: "h2", PlanVersion: 1, State: "active"}}
			_, err := s.Observe(ctx, 0, o)
			require.NoError(t, err)
			r := SolRecovery{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "request", ReceiptID: "requested", ActualModel: TierSol, ActualModelReceipt: "actual", Status: "requested", StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, AffectedTaskIDs: []string{"t"}}
			require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
			r.Status, r.ReceiptID = "started", "started"
			require.NoError(t, tc.prepare(&s, &o, &r))
			before, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
			require.NoError(t, err)
			require.True(t, ok)
			beforeBody, err := json.Marshal(before.Body)
			require.NoError(t, err)
			beforeLog, err := s.Durable.ListMutations(ctx, "w")
			require.NoError(t, err)
			require.Error(t, s.RecordSolRecovery(ctx, 0, "w", r))
			after, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
			require.NoError(t, err)
			require.True(t, ok)
			afterBody, err := json.Marshal(after.Body)
			require.NoError(t, err)
			require.Equal(t, before.SHA256, after.SHA256)
			require.Equal(t, before.UpdatedAt, after.UpdatedAt)
			require.JSONEq(t, string(beforeBody), string(afterBody))
			afterLog, err := s.Durable.ListMutations(ctx, "w")
			require.NoError(t, err)
			require.Equal(t, len(beforeLog), len(afterLog))
			require.True(t, reflect.DeepEqual(beforeLog, afterLog))
		})
	}

	t.Run("valid requested to started preserves identity", func(t *testing.T) {
		s := testStore(t)
		o := observation("origin", true)
		o.Tasks = []Task{{ID: "t", Head: "h", PlanVersion: 1, State: "active"}}
		_, err := s.Observe(ctx, 0, o)
		require.NoError(t, err)
		r := SolRecovery{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "request", ReceiptID: "requested", ActualModel: TierSol, ActualModelReceipt: "actual", Status: "requested", StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, AffectedTaskIDs: []string{"t"}}
		require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
		requested := r
		before, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
		require.NoError(t, err)
		require.True(t, ok)
		beforeBody, err := json.Marshal(before.Body)
		require.NoError(t, err)
		beforeLog, err := s.Durable.ListMutations(ctx, "w")
		require.NoError(t, err)
		r.Status, r.ReceiptID = "started", "started"
		require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
		after, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
		require.NoError(t, err)
		require.True(t, ok)
		afterBody, err := json.Marshal(after.Body)
		require.NoError(t, err)
		afterLog, err := s.Durable.ListMutations(ctx, "w")
		require.NoError(t, err)
		require.Len(t, afterLog, len(beforeLog)+1)
		require.NotEqual(t, before.SHA256, after.SHA256)
		require.NotEqual(t, before.UpdatedAt, after.UpdatedAt)
		require.NotEqual(t, string(beforeBody), string(afterBody))
		st, _, err := s.load(ctx, "w")
		require.NoError(t, err)
		stored := st.Recoveries["incident/1/1"]
		require.Equal(t, "started", stored.Status)
		require.Equal(t, "started", stored.ReceiptID)
		require.Equal(t, []string{"t"}, stored.AffectedTaskIDs)
		require.Equal(t, requested.IncidentID, stored.IncidentID)
		require.Equal(t, requested.EventID, stored.EventID)
		require.Equal(t, requested.EvidenceID, stored.EvidenceID)
		require.Equal(t, requested.RequestID, stored.RequestID)
		require.Equal(t, requested.ActualModel, stored.ActualModel)
		require.Equal(t, requested.ActualModelReceipt, stored.ActualModelReceipt)
		require.Equal(t, requested.StrategyVersion, stored.StrategyVersion)
		require.Equal(t, requested.PlanVersion, stored.PlanVersion)
		require.Equal(t, requested.CompletedAt, stored.CompletedAt)
		require.Equal(t, requested.AffectedTaskIDs, stored.AffectedTaskIDs)
		require.Equal(t, requested, st.RecoveryReceipts["requested"])
		require.Equal(t, r, st.RecoveryReceipts["started"])
	})
}

func TestRecoveryTransitionReceiptReplayAfterNewerObservationIsImmutable(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	o := observation("origin", true)
	o.Tasks = []Task{{ID: "t", Head: "h", PlanVersion: 1, State: "active"}}
	_, err := s.Observe(ctx, 0, o)
	require.NoError(t, err)
	r := SolRecovery{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "request", ReceiptID: "requested", ActualModel: TierSol, ActualModelReceipt: "actual", Status: "requested", StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, AffectedTaskIDs: []string{"t"}}
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	r.Status, r.ReceiptID = "started", "started"
	started := r
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	r.Status, r.ReceiptID = "completed", "completed"
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	r.Status, r.ReceiptID, r.Accepted = "decision_accepted", "accepted", true
	r.ProposedAction, r.ExpectedEffect, r.EffectDueAt = "run", "pass", o.ObservedAt.Add(time.Hour)
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	next := o
	next.EventID, next.EvidenceID = "newer", "newer-evidence"
	next.ObservedAt = o.ObservedAt.Add(time.Minute)
	_, err = s.Observe(ctx, 0, next)
	require.NoError(t, err)
	before, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
	require.NoError(t, err)
	require.True(t, ok)
	beforeBody, err := json.Marshal(before.Body)
	require.NoError(t, err)
	beforeLog, err := s.Durable.ListMutations(ctx, "w")
	require.NoError(t, err)
	beforeState, _, err := s.load(ctx, "w")
	require.NoError(t, err)
	beforeRecovery := beforeState.Recoveries["incident/1/1"]
	beforeLedger := beforeState.RecoveryReceipts

	// RED: the pre-fix code rejects this because Last is newer than started.
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", started))
	afterReplay, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
	require.NoError(t, err)
	require.True(t, ok)
	afterReplayBody, err := json.Marshal(afterReplay.Body)
	require.NoError(t, err)
	afterReplayLog, err := s.Durable.ListMutations(ctx, "w")
	require.NoError(t, err)
	afterReplayState, _, err := s.load(ctx, "w")
	require.NoError(t, err)
	require.Equal(t, before.SHA256, afterReplay.SHA256)
	require.Equal(t, before.UpdatedAt, afterReplay.UpdatedAt)
	require.JSONEq(t, string(beforeBody), string(afterReplayBody))
	require.Equal(t, beforeLog, afterReplayLog)
	require.Equal(t, beforeRecovery, afterReplayState.Recoveries["incident/1/1"])
	require.Equal(t, beforeLedger, afterReplayState.RecoveryReceipts)

	altered := started
	altered.ExpectedEffect = "altered"
	require.ErrorIs(t, s.RecordSolRecovery(ctx, 0, "w", altered), ErrStaleContract)
	afterConflict, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
	require.NoError(t, err)
	require.True(t, ok)
	afterConflictBody, err := json.Marshal(afterConflict.Body)
	require.NoError(t, err)
	afterConflictLog, err := s.Durable.ListMutations(ctx, "w")
	require.NoError(t, err)
	afterConflictState, _, err := s.load(ctx, "w")
	require.NoError(t, err)
	require.Equal(t, before.SHA256, afterConflict.SHA256)
	require.Equal(t, before.UpdatedAt, afterConflict.UpdatedAt)
	require.JSONEq(t, string(beforeBody), string(afterConflictBody))
	require.Equal(t, beforeLog, afterConflictLog)
	require.Equal(t, beforeRecovery, afterConflictState.Recoveries["incident/1/1"])
	require.Equal(t, beforeLedger, afterConflictState.RecoveryReceipts)
}

func TestRecoveryEffectReceiptRequiresExactCurrentEvidence(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		prepare func(*Store, *Observation, *RecoveryEffectReceipt) error
	}{
		{name: "original trigger", prepare: func(_ *Store, o *Observation, r *RecoveryEffectReceipt) error {
			r.EventID, r.EvidenceID = "origin", "origin-evidence"
			return nil
		}},
		{name: "incomplete last", prepare: func(s *Store, o *Observation, r *RecoveryEffectReceipt) error {
			next := *o
			next.EventID, next.EvidenceID, next.Complete, next.ObservedAt = "incomplete", "incomplete-evidence", false, o.ObservedAt.Add(time.Minute)
			_, err := s.Observe(ctx, 0, next)
			r.EventID, r.EvidenceID, r.ObservedAt = next.EventID, next.EvidenceID, next.ObservedAt
			return err
		}},
		{name: "stale receipt time", prepare: func(_ *Store, o *Observation, r *RecoveryEffectReceipt) error {
			r.ObservedAt = o.ObservedAt.Add(-time.Second)
			return nil
		}},
		{name: "wrong task", prepare: func(_ *Store, _ *Observation, r *RecoveryEffectReceipt) error { r.TaskID = "other"; return nil }},
		{name: "wrong head", prepare: func(_ *Store, _ *Observation, r *RecoveryEffectReceipt) error { r.Head = "other"; return nil }},
		{name: "wrong plan", prepare: func(_ *Store, _ *Observation, r *RecoveryEffectReceipt) error { r.PlanVersion = 2; return nil }},
		{name: "wrong strategy", prepare: func(_ *Store, _ *Observation, r *RecoveryEffectReceipt) error { r.StrategyVersion = 2; return nil }},
		{name: "missing verifier", prepare: func(_ *Store, _ *Observation, r *RecoveryEffectReceipt) error { r.Verifier = ""; return nil }},
		{name: "missing milestone", prepare: func(_ *Store, _ *Observation, r *RecoveryEffectReceipt) error { r.Milestone = ""; return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, o, receipt := effectReceiptFixture(t, ctx)
			require.NoError(t, tc.prepare(&s, &o, &receipt))
			before, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
			require.NoError(t, err)
			require.True(t, ok)
			beforeBody, _ := json.Marshal(before.Body)
			beforeLog, err := s.Durable.ListMutations(ctx, "w")
			require.NoError(t, err)
			beforeState, _, err := s.load(ctx, "w")
			require.NoError(t, err)
			require.ErrorIs(t, s.VerifySolRecoveryEffectReceipt(ctx, 0, "w", receipt), ErrStaleContract)
			after, ok, err := s.Durable.GetRecord(ctx, "w", stateRecordID)
			require.NoError(t, err)
			require.True(t, ok)
			afterBody, _ := json.Marshal(after.Body)
			afterLog, err := s.Durable.ListMutations(ctx, "w")
			require.NoError(t, err)
			afterState, _, err := s.load(ctx, "w")
			require.NoError(t, err)
			require.Equal(t, before.SHA256, after.SHA256)
			require.Equal(t, before.UpdatedAt, after.UpdatedAt)
			require.JSONEq(t, string(beforeBody), string(afterBody))
			require.Equal(t, beforeLog, afterLog)
			require.Equal(t, beforeState.Recoveries, afterState.Recoveries)
			require.Equal(t, beforeState.RecoveryReceipts, afterState.RecoveryReceipts)
		})
	}
	t.Run("valid current evidence", func(t *testing.T) {
		s, _, receipt := effectReceiptFixture(t, ctx)
		require.NoError(t, s.VerifySolRecoveryEffectReceipt(ctx, 0, "w", receipt))
		st, _, err := s.load(ctx, "w")
		require.NoError(t, err)
		require.Equal(t, "effect_verified", st.Recoveries["incident/1/1"].Status)
	})
}

func effectReceiptFixture(t *testing.T, ctx context.Context) (Store, Observation, RecoveryEffectReceipt) {
	t.Helper()
	s := testStore(t)
	o := observation("origin", true)
	o.Tasks = []Task{{ID: "t", Head: "h", PlanVersion: 1, State: "active"}}
	_, err := s.Observe(ctx, 0, o)
	require.NoError(t, err)
	r := SolRecovery{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, RequestID: "request", ReceiptID: "accepted", ProposedAction: "run", ExpectedEffect: "pass", ActualModel: TierSol, ActualModelReceipt: "actual", Status: "decision_accepted", Accepted: true, StrategyVersion: 1, PlanVersion: 1, CompletedAt: o.ObservedAt, EffectDueAt: o.ObservedAt.Add(time.Hour), AffectedTaskIDs: []string{"t"}}
	require.NoError(t, s.RecordSolRecovery(ctx, 0, "w", r))
	o.EventID, o.EvidenceID, o.ObservedAt = "effect", "effect-evidence", o.ObservedAt.Add(time.Minute)
	_, err = s.Observe(ctx, 0, o)
	require.NoError(t, err)
	return s, o, RecoveryEffectReceipt{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, TaskID: "t", Head: "h", PlanVersion: 1, StrategyVersion: 1, Verifier: "verifier", Milestone: "milestone", ObservedAt: o.ObservedAt}
}
