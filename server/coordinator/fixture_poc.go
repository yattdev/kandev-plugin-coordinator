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
var ErrFixtureOutcomeUnknown = errors.New("fixture POC: operation outcome unknown")

type FixturePOCReport struct {
	Decision governor.Result `json:"decision"`
	Grant    FixtureGrant    `json:"grant"`
	Readback FixtureReadback `json:"readback"`
	Before   []governor.Task `json:"before"`
	After    []governor.Task `json:"after"`
	Denials  []FixtureDenial `json:"denials"`
}
type FixtureDenial struct {
	Reason   string `json:"reason"`
	TargetID string `json:"target_id"`
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
type fixtureBoard struct {
	tasks     map[string]governor.Task
	mutations int
	reader    *fixtureTaskReader
}
type fixtureTaskReader struct{ rows map[string]pluginsdk.Task }

var _ pluginsdk.TaskReader = (*fixtureTaskReader)(nil)

func newFixtureTaskReader() *fixtureTaskReader {
	return &fixtureTaskReader{rows: map[string]pluginsdk.Task{"done-dependency": {ID: "done-dependency", WorkspaceID: "fixture-workspace", State: "Done"}, "blocked-target": {ID: "blocked-target", WorkspaceID: "fixture-workspace", State: "Blocked", Metadata: map[string]any{"dependencies": []string{"done-dependency"}}}, "in-progress": {ID: "in-progress", WorkspaceID: "fixture-workspace", State: "InProgress"}}}
}
func (r *fixtureTaskReader) List(_ context.Context, _ pluginsdk.TaskFilter, page pluginsdk.Page) ([]pluginsdk.Task, *pluginsdk.PageInfo, error) {
	out := make([]pluginsdk.Task, 0, len(r.rows))
	for _, t := range r.rows {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if page.Cursor == "" && len(out) > 1 {
		return out[:1], &pluginsdk.PageInfo{HasMore: true, NextCursor: "1"}, nil
	}
	if page.Cursor == "1" {
		return out[1:], &pluginsdk.PageInfo{}, nil
	}
	return nil, nil, ErrFixtureGrantDenied
}
func (r *fixtureTaskReader) Get(_ context.Context, id string) (*pluginsdk.Task, error) {
	t, ok := r.rows[id]
	if !ok {
		return nil, ErrFixtureGrantDenied
	}
	return &t, nil
}
func (r *fixtureTaskReader) Create(context.Context, pluginsdk.CreateTaskInput) (*pluginsdk.Task, error) {
	return nil, ErrFixtureGrantDenied
}
func (r *fixtureTaskReader) Update(_ context.Context, in pluginsdk.UpdateTaskInput) (*pluginsdk.Task, error) {
	t, ok := r.rows[in.ID]
	if !ok || in.State == nil {
		return nil, ErrFixtureGrantDenied
	}
	t.State = *in.State
	r.rows[in.ID] = t
	return &t, nil
}

func newFixtureBoard() *fixtureBoard {
	b := &fixtureBoard{tasks: map[string]governor.Task{}, reader: newFixtureTaskReader()}
	for _, t := range fixtureObservation("x", "x", time.Now(), "Blocked").Tasks {
		b.tasks[t.ID] = t
	}
	return b
}
func loadFixtureBoard(ctx context.Context, store *durablestate.Store) (*fixtureBoard, error) {
	b := newFixtureBoard()
	rec, found, err := store.GetRecord(ctx, "fixture-workspace", "fixture-operation-1")
	if err != nil || !found {
		return b, err
	}
	target, ok := rec.Body["target"].(string)
	state, sok := rec.Body["state"].(string)
	verified, vok := rec.Body["verified"].(bool)
	if !vok || !verified {
		return nil, ErrFixtureOutcomeUnknown
	}
	if !ok || !sok || target != "blocked-target" || state != "Ready" {
		return nil, ErrFixtureGrantDenied
	}
	t := b.tasks[target]
	t.State = state
	t.BlockerReason = ""
	b.tasks[target] = t
	stateCopy := state
	if _, err := b.reader.Update(ctx, pluginsdk.UpdateTaskInput{ID: target, State: &stateCopy}); err != nil {
		return nil, err
	}
	b.mutations = 1
	return b, nil
}
func (b *fixtureBoard) Read(ctx context.Context) ([]governor.Task, error) {
	if b.reader != nil {
		var rows []pluginsdk.Task
		cursor := ""
		for {
			pageRows, info, err := b.reader.List(ctx, pluginsdk.TaskFilter{}, pluginsdk.Page{Cursor: cursor})
			if err != nil {
				return nil, err
			}
			rows = append(rows, pageRows...)
			if info == nil || !info.HasMore {
				break
			}
			if info.NextCursor == "" || info.NextCursor == cursor {
				return nil, ErrFixtureGrantDenied
			}
			cursor = info.NextCursor
		}
		out := make([]governor.Task, 0, len(rows))
		for _, row := range rows {
			task, ok := b.tasks[row.ID]
			if !ok {
				return nil, fmt.Errorf("fixture POC: unknown listed task %q", row.ID)
			}
			task.State = row.State
			if deps, ok := row.Metadata["dependencies"].([]string); ok {
				task.Dependencies = deps
			}
			out = append(out, task)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	out := make([]governor.Task, 0, len(b.tasks))
	for _, t := range b.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (b *fixtureBoard) Apply(target, action string) error {
	if action != fixtureAction {
		return ErrFixtureGrantDenied
	}
	t, ok := b.tasks[target]
	if !ok || t.State != "Blocked" {
		return ErrFixtureGrantDenied
	}
	t.State = "Ready"
	if b.reader != nil {
		state := "Ready"
		if _, err := b.reader.Update(context.Background(), pluginsdk.UpdateTaskInput{ID: target, State: &state}); err != nil {
			return err
		}
	}
	t.BlockerReason = ""
	b.tasks[target] = t
	b.mutations++
	return nil
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
	board := newFixtureBoard()
	origin := fixtureObservation("fixture-origin", "fixture-evidence-origin", time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), "Blocked")
	tasks, err := board.Read(ctx)
	origin.Tasks = tasks
	if err != nil {
		return FixturePOCReport{}, err
	}
	decision, err := fixtureActionCall(ctx, p, origin)
	if err != nil {
		return FixturePOCReport{}, err
	}
	target, err := fixtureTarget(decision)
	if err != nil {
		return FixturePOCReport{}, err
	}
	grant := FixtureGrant{ID: "fixture-grant-1", TargetID: target, Action: fixtureAction}
	denials := []FixtureDenial{}
	for _, trial := range []struct {
		reason, target string
		revoked        bool
	}{{"competing_target", "in-progress", false}, {"revoked_grant", target, true}} {
		g := grant
		g.Revoked = trial.revoked
		if _, err := applyFixtureGrant(ctx, p, store, board, &g, origin, "denied-"+trial.reason, trial.target, fixtureAction); !errors.Is(err, ErrFixtureGrantDenied) {
			return FixturePOCReport{}, fmt.Errorf("fixture POC: expected denial")
		}
		denials = append(denials, FixtureDenial{trial.reason, trial.target})
	}
	board, err = loadFixtureBoard(ctx, store.Durable)
	if err != nil {
		return FixturePOCReport{}, err
	}
	readback, err := applyFixtureGrant(ctx, p, store, board, &grant, origin, "fixture-operation-1", target, fixtureAction)
	if err != nil {
		return FixturePOCReport{}, err
	}
	after, err := board.Read(ctx)
	if err != nil {
		return FixturePOCReport{}, err
	}
	return FixturePOCReport{Decision: decision, Grant: grant, Readback: readback, Before: origin.Tasks, After: after, Denials: denials}, nil
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

func applyFixtureGrant(ctx context.Context, p *Plugin, store governor.Store, board *fixtureBoard, grant *FixtureGrant, origin governor.Observation, operationID, target, action string) (FixtureReadback, error) {
	if grant == nil || grant.Revoked || grant.TargetID != target || grant.Action != action {
		return FixtureReadback{}, ErrFixtureGrantDenied
	}
	if prior, found, err := store.Durable.GetRecord(ctx, origin.WorkspaceID, operationID); err != nil {
		return FixtureReadback{}, err
	} else if found {
		state, sok := prior.Body["state"].(string)
		receipt, rok := prior.Body["receipt"].(string)
		savedTarget, tok := prior.Body["target"].(string)
		savedAction, aok := prior.Body["action"].(string)
		savedGrant, gok := prior.Body["grant"].(string)
		verified, vok := prior.Body["verified"].(bool)
		if !sok || !rok || !tok || !aok || !gok || !vok || !verified || savedTarget != target || savedAction != action || savedGrant != grant.ID {
			return FixtureReadback{}, ErrFixtureGrantDenied
		}
		return FixtureReadback{OperationID: operationID, TaskID: target, State: state, Receipt: receipt, Replay: true}, nil
	}
	grant.Used = true
	if err := board.Apply(target, action); err != nil {
		return FixtureReadback{}, err
	}
	projection := map[string]any{"state": "Ready", "receipt": "fixture-evidence-effect", "target": target, "action": action, "grant": grant.ID, "operation": operationID, "verified": false}
	if _, err := store.Durable.AppendAdd(ctx, origin.WorkspaceID, 0, operationID, durablestate.KindDoneReceipt, projection, durablestate.StorageInline); err != nil {
		return FixtureReadback{}, err
	}
	recovery := governor.SolRecovery{IncidentID: "fixture-incident", EventID: origin.EventID, EvidenceID: origin.EvidenceID, RequestID: operationID, ReceiptID: operationID + "/accepted", ProposedAction: action, ExpectedEffect: "task becomes Ready", ActualModel: governor.TierSol, ActualModelReceipt: grant.ID, Status: "decision_accepted", Accepted: true, StrategyVersion: origin.StrategyVersion, PlanVersion: origin.PlanVersion, CompletedAt: origin.ObservedAt, EffectDueAt: origin.ObservedAt.Add(time.Hour), AffectedTaskIDs: []string{target}}
	if err := store.RecordSolRecovery(ctx, 0, origin.WorkspaceID, recovery); err != nil {
		return FixtureReadback{}, err
	}
	effect := fixtureObservation("fixture-effect", "fixture-evidence-effect", origin.ObservedAt.Add(time.Minute), "Ready")
	tasks, readErr := board.Read(ctx)
	effect.Tasks = tasks
	if readErr != nil {
		return FixtureReadback{}, readErr
	}
	if _, err := fixtureActionCall(ctx, p, effect); err != nil {
		return FixtureReadback{}, err
	}
	receipt := governor.RecoveryEffectReceipt{IncidentID: recovery.IncidentID, EventID: effect.EventID, EvidenceID: effect.EvidenceID, TaskID: target, Head: "fixture-head", PlanVersion: effect.PlanVersion, StrategyVersion: effect.StrategyVersion, Verifier: "fixture-readback", Milestone: "Ready", ObservedAt: effect.ObservedAt}
	if err := store.VerifySolRecoveryEffectReceipt(ctx, 0, origin.WorkspaceID, receipt); err != nil {
		return FixtureReadback{}, err
	}
	readback := FixtureReadback{OperationID: operationID, TaskID: target, State: "Ready", Receipt: receipt.EvidenceID}
	projection["verified"] = true
	_, err := store.Durable.AppendUpdate(ctx, origin.WorkspaceID, 0, operationID, projection, durablestate.StorageInline)
	if err != nil {
		return FixtureReadback{}, err
	}
	return readback, nil
}

func fixtureObservation(event, evidence string, observed time.Time, targetState string) governor.Observation {
	return governor.Observation{SchemaVersion: governor.SchemaVersion, WorkspaceID: "fixture-workspace", ObservedAt: observed, EventID: event, EvidenceID: evidence, Provenance: "stage-0-fixture", Complete: true, StrategyVersion: 1, PlanVersion: 1, Tasks: []governor.Task{
		{ID: "done-dependency", State: "Done", Head: "fixture-head", PlanVersion: 1},
		{ID: "blocked-target", State: targetState, Dependencies: []string{"done-dependency"}, BlockerReason: map[bool]string{true: "fixture dependency cleared"}[targetState == "Blocked"], Head: "fixture-head", PlanVersion: 1},
		{ID: "in-progress", State: "InProgress", Head: "fixture-head", PlanVersion: 1},
	}}
}
