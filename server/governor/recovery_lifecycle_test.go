package governor

import (
	"context"
	"encoding/json"
	"path/filepath"
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
	require.NoError(t, s.VerifySolRecoveryEffectReceipt(ctx, 0, "w", RecoveryEffectReceipt{IncidentID: "incident", EventID: o.EventID, EvidenceID: o.EvidenceID, TaskID: "t", Head: "h", PlanVersion: 1, StrategyVersion: 1, Verifier: "v", Milestone: "m"}))
	err = s.RecordSolRecurrenceReceipt(ctx, 0, "w", RecoveryRecurrenceReceipt{IncidentID: "incident", EventID: "wrong", EvidenceID: o.EvidenceID, StrategyVersion: 1, PlanVersion: 1, RecurrenceID: "r", ReceiptID: "wrong", ObservedAt: o.ObservedAt})
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
	require.NoError(t, s.VerifySolRecoveryEffectReceipt(ctx, 0, "w", RecoveryEffectReceipt{IncidentID: "i", EventID: o.EventID, EvidenceID: o.EvidenceID, TaskID: "t", Head: "h", PlanVersion: 1, StrategyVersion: 1, Verifier: "v", Milestone: "m"}))
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
