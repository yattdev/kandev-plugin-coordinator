package coordinator

// This file is deliberately a local, synthetic demonstration. It sends its
// observations through HandleAction and ShadowStoreObserver; it never talks to
// a Host board, provider, credential service, or scheduler.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kandev/kandev/pkg/pluginsdk"
	"kandev-plugin-coordinator/server/durablestate"
	"kandev-plugin-coordinator/server/governor"
)

const fixtureAction = "fixture.unblock"

var ErrFixtureGrantDenied = errors.New("fixture POC: grant denied")

type FixturePOCReport struct {
	Decision governor.Result `json:"decision"`
	Grant    FixtureGrant    `json:"grant"`
	Readback FixtureReadback `json:"readback"`
}

type FixtureGrant struct {
	ID, TargetID, Action string
	Revoked, Used        bool
}

type FixtureReadback struct {
	OperationID string `json:"operation_id"`
	TaskID      string `json:"task_id"`
	State       string `json:"state"`
	Receipt     string `json:"receipt"`
	Replay      bool   `json:"replay"`
}

// RunFixturePOC is the runnable Stage 0 driver. Its two snapshots traverse
// Plugin.HandleAction -> ActionShadowObserve -> ShadowStoreObserver ->
// governor.Store, so an action, decision, or receipt seam regression fails the
// demonstration.
func RunFixturePOC(ctx context.Context) (FixturePOCReport, error) {
	dir, err := os.MkdirTemp("", "kandev-coordinator-fixture-")
	if err != nil {
		return FixturePOCReport{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "fixture-poc.db")
	store, err := durablestate.Open(path)
	if err != nil {
		return FixturePOCReport{}, err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return FixturePOCReport{}, err
	}
	return runFixturePOC(ctx, governor.Store{Durable: store, Now: func() time.Time { return time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC) }})
}

func runFixturePOC(ctx context.Context, store governor.Store) (FixturePOCReport, error) {
	p := New()
	p.SetShadowObserver(ShadowStoreObserver{Store: &store})
	origin := fixtureObservation("fixture-origin", "fixture-evidence-origin", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	decision, err := fixtureActionCall(ctx, p, origin)
	if err != nil {
		return FixturePOCReport{}, err
	}
	target, err := fixtureTarget(decision)
	if err != nil {
		return FixturePOCReport{}, err
	}
	grant := FixtureGrant{ID: "fixture-grant-1", TargetID: target, Action: fixtureAction}
	readback, err := applyFixtureGrant(ctx, p, store, &grant, origin, "fixture-operation-1", target, fixtureAction)
	if err != nil {
		return FixturePOCReport{}, err
	}
	return FixturePOCReport{Decision: decision, Grant: grant, Readback: readback}, nil
}

func fixtureActionCall(ctx context.Context, p *Plugin, observation governor.Observation) (governor.Result, error) {
	body, err := json.Marshal(observation)
	if err != nil {
		return governor.Result{}, err
	}
	response, err := p.HandleAction(ctx, &pluginsdk.PluginActionRequest{ActionKey: ActionShadowObserve, Context: pluginsdk.VerifiedActionContext{WorkspaceID: observation.WorkspaceID}, Body: body})
	if err != nil {
		return governor.Result{}, err
	}
	var decoded struct {
		Status string          `json:"status"`
		Result governor.Result `json:"result"`
	}
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		return governor.Result{}, err
	}
	if decoded.Status != "shadow" {
		return governor.Result{}, fmt.Errorf("fixture POC: unexpected action status %q", decoded.Status)
	}
	return decoded.Result, nil
}

func fixtureTarget(result governor.Result) (string, error) {
	var targets []string
	for _, attention := range result.Digest.Attention {
		if attention.Reason == "blocked" && attention.TaskID != "" {
			targets = append(targets, attention.TaskID)
		}
	}
	sort.Strings(targets)
	if len(targets) != 1 {
		return "", fmt.Errorf("fixture POC: expected one blocked target, got %d", len(targets))
	}
	return targets[0], nil
}

func applyFixtureGrant(ctx context.Context, p *Plugin, store governor.Store, grant *FixtureGrant, origin governor.Observation, operationID, target, action string) (FixtureReadback, error) {
	if grant == nil || grant.Revoked || grant.TargetID != target || grant.Action != action {
		return FixtureReadback{}, ErrFixtureGrantDenied
	}
	if grant.Used {
		return FixtureReadback{OperationID: operationID, TaskID: target, State: "Ready", Receipt: "fixture-evidence-effect", Replay: true}, nil
	}
	grant.Used = true
	recovery := governor.SolRecovery{IncidentID: "fixture-incident", EventID: origin.EventID, EvidenceID: origin.EvidenceID, RequestID: operationID, ReceiptID: operationID + "/accepted", ProposedAction: action, ExpectedEffect: "task becomes Ready", ActualModel: governor.TierSol, ActualModelReceipt: grant.ID, Status: "decision_accepted", Accepted: true, StrategyVersion: origin.StrategyVersion, PlanVersion: origin.PlanVersion, CompletedAt: origin.ObservedAt, EffectDueAt: origin.ObservedAt.Add(time.Hour), AffectedTaskIDs: []string{target}}
	if err := store.RecordSolRecovery(ctx, 0, origin.WorkspaceID, recovery); err != nil {
		return FixtureReadback{}, err
	}
	effect := fixtureObservation("fixture-effect", "fixture-evidence-effect", origin.ObservedAt.Add(time.Minute), "Ready")
	if _, err := fixtureActionCall(ctx, p, effect); err != nil {
		return FixtureReadback{}, err
	}
	receipt := governor.RecoveryEffectReceipt{IncidentID: recovery.IncidentID, EventID: effect.EventID, EvidenceID: effect.EvidenceID, TaskID: target, Head: "fixture-head", PlanVersion: effect.PlanVersion, StrategyVersion: effect.StrategyVersion, Verifier: "fixture-readback", Milestone: "Ready", ObservedAt: effect.ObservedAt}
	if err := store.VerifySolRecoveryEffectReceipt(ctx, 0, origin.WorkspaceID, receipt); err != nil {
		return FixtureReadback{}, err
	}
	return FixtureReadback{OperationID: operationID, TaskID: target, State: "Ready", Receipt: receipt.EvidenceID}, nil
}

func fixtureObservation(event, evidence string, observed time.Time, targetState string) governor.Observation {
	return governor.Observation{SchemaVersion: governor.SchemaVersion, WorkspaceID: "fixture-workspace", ObservedAt: observed, EventID: event, EvidenceID: evidence, Provenance: "stage-0-fixture", Complete: true, StrategyVersion: 1, PlanVersion: 1, Tasks: []governor.Task{
		{ID: "done-dependency", State: "Done", Head: "fixture-head", PlanVersion: 1},
		{ID: "blocked-target", State: targetState, Dependencies: []string{"done-dependency"}, BlockerReason: map[bool]string{true: "fixture dependency cleared"}[targetState == "Blocked"], Head: "fixture-head", PlanVersion: 1},
		{ID: "in-progress", State: "InProgress", Head: "fixture-head", PlanVersion: 1},
	}}
}
