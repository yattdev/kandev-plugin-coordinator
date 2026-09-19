package governor

import (
	"context"
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
