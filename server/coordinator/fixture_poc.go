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
	Decision       governor.Result `json:"decision"`
	SelectedAction string          `json:"selected_action"`
	Grant          FixtureGrant    `json:"grant"`
	Readback       FixtureReadback `json:"readback"`
	Before         []governor.Task `json:"before"`
	After          []governor.Task `json:"after"`
	Denials        []FixtureDenial `json:"denials"`
}
type FixtureDenial struct {
	Reason   string `json:"reason"`
	TargetID string `json:"target_id"`
	Action   string `json:"action"`
}

type FixtureGrant struct {
	ID         string `json:"id"`
	TargetID   string `json:"target_id"`
	Action     string `json:"action"`
	EvidenceID string `json:"evidence_id"`
	Revoked    bool   `json:"revoked"`
	Used       bool   `json:"used"`
}

type FixtureReadback struct {
	OperationID string `json:"operation_id"`
	TaskID      string `json:"task_id"`
	State       string `json:"state"`
	Receipt     string `json:"receipt"`
	Replay      bool   `json:"replay"`
}
type fixtureBoard struct {
	tasks      map[string]governor.Task
	mutations  int
	host       *fixtureHost
	reader     pluginsdk.TaskReader
	taskReader *fixtureTaskReader
}
type fixtureTaskReader struct {
	rows      map[string]pluginsdk.Task
	listErr   error
	badCursor bool
	durable   *durablestate.Store
}

var _ pluginsdk.TaskReader = (*fixtureTaskReader)(nil)

// fixtureHost is a local typed Host double. The POC reaches the task adapter
// only through Host.Tasks(), exercising the SDK accessor boundary without a
// broker, Host process, or live board.
type fixtureHost struct {
	pluginsdk.UnimplementedHostData
	tasks pluginsdk.TaskReader
}

func (h *fixtureHost) GetState(context.Context, string, string, string) (map[string]any, bool, error) {
	return nil, false, nil
}
func (h *fixtureHost) SetState(context.Context, string, string, string, map[string]any) error {
	return nil
}
func (h *fixtureHost) DeleteState(context.Context, string, string, string) error { return nil }
func (h *fixtureHost) ListState(context.Context, string, string) ([]pluginsdk.StateEntry, error) {
	return nil, nil
}
func (h *fixtureHost) GetConfig(context.Context) (map[string]any, error) {
	return map[string]any{}, nil
}
func (h *fixtureHost) RevealSecret(context.Context, string) (string, error) {
	return "", ErrFixtureGrantDenied
}
func (h *fixtureHost) GetSecret(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (h *fixtureHost) SetSecret(context.Context, string, string) error { return ErrFixtureGrantDenied }
func (h *fixtureHost) DeleteSecret(context.Context, string) error      { return ErrFixtureGrantDenied }
func (h *fixtureHost) EmitEvent(context.Context, string, map[string]any) error {
	return ErrFixtureGrantDenied
}
func (h *fixtureHost) Tasks() pluginsdk.TaskReader { return h.tasks }

var _ pluginsdk.Host = (*fixtureHost)(nil)

func newFixtureTaskReader(ctx context.Context, durable *durablestate.Store) (*fixtureTaskReader, error) {
	r := &fixtureTaskReader{durable: durable, rows: map[string]pluginsdk.Task{"done-dependency": {ID: "done-dependency", WorkspaceID: "fixture-workspace", State: "Done"}, "blocked-target": {ID: "blocked-target", WorkspaceID: "fixture-workspace", State: "Blocked", Metadata: map[string]any{"dependencies": []string{"done-dependency"}}}, "in-progress": {ID: "in-progress", WorkspaceID: "fixture-workspace", State: "InProgress"}}}
	if durable == nil {
		return r, nil
	}
	for id, task := range r.rows {
		record, found, err := durable.GetRecord(ctx, "fixture-workspace", fixtureBoardRecordID(id))
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		recordedID, idOK := record.Body["task_id"].(string)
		state, stateOK := record.Body["state"].(string)
		if !idOK || !stateOK || recordedID != id || (state != task.State && state != "Ready") {
			return nil, ErrFixtureOutcomeUnknown
		}
		task.State = state
		r.rows[id] = task
	}
	return r, nil
}
func (r *fixtureTaskReader) List(_ context.Context, _ pluginsdk.TaskFilter, page pluginsdk.Page) ([]pluginsdk.Task, *pluginsdk.PageInfo, error) {
	if r.listErr != nil {
		return nil, nil, r.listErr
	}
	out := make([]pluginsdk.Task, 0, len(r.rows))
	for _, t := range r.rows {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if page.Cursor == "" && r.badCursor {
		return out[:1], &pluginsdk.PageInfo{HasMore: true}, nil
	}
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
func (r *fixtureTaskReader) Update(ctx context.Context, in pluginsdk.UpdateTaskInput) (*pluginsdk.Task, error) {
	t, ok := r.rows[in.ID]
	if !ok || in.State == nil {
		return nil, ErrFixtureGrantDenied
	}
	if r.durable != nil {
		body := map[string]any{"task_id": in.ID, "state": *in.State}
		_, found, err := r.durable.GetRecord(ctx, "fixture-workspace", fixtureBoardRecordID(in.ID))
		if err != nil {
			return nil, err
		}
		if found {
			_, err = r.durable.AppendUpdate(ctx, "fixture-workspace", 0, fixtureBoardRecordID(in.ID), body, durablestate.StorageInline)
		} else {
			_, err = r.durable.AppendAdd(ctx, "fixture-workspace", 0, fixtureBoardRecordID(in.ID), durablestate.KindDirtyTask, body, durablestate.StorageInline)
		}
		if err != nil {
			return nil, err
		}
	}
	t.State = *in.State
	r.rows[in.ID] = t
	return &t, nil
}

func fixtureBoardRecordID(taskID string) string { return "fixture-board/" + taskID }

func newFixtureBoard(ctx context.Context, durable *durablestate.Store) (*fixtureBoard, error) {
	reader, err := newFixtureTaskReader(ctx, durable)
	if err != nil {
		return nil, err
	}
	host := &fixtureHost{tasks: reader}
	b := &fixtureBoard{tasks: map[string]governor.Task{}, host: host, reader: host.Tasks(), taskReader: reader}
	for _, t := range fixtureObservation("x", "x", time.Now(), "Blocked").Tasks {
		b.tasks[t.ID] = t
	}
	return b, nil
}
func loadFixtureBoard(ctx context.Context, store *durablestate.Store, operationID string) (*fixtureBoard, error) {
	b, err := newFixtureBoard(ctx, store)
	if err != nil {
		return nil, err
	}
	rec, found, err := store.GetRecord(ctx, "fixture-workspace", operationID)
	if err != nil || !found {
		return b, err
	}
	verified, vok := rec.Body["verified"].(bool)
	phase, pok := rec.Body["phase"].(string)
	if !vok || (!verified && (!pok || phase != "pending")) {
		return nil, ErrFixtureOutcomeUnknown
	}
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
			if row.State != "Blocked" {
				task.BlockerReason = ""
			}
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
	board, err := newFixtureBoard(ctx, store.Durable)
	if err != nil {
		return FixturePOCReport{}, err
	}
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
	grant := FixtureGrant{ID: "fixture-grant-1", TargetID: target, Action: fixtureAction, EvidenceID: origin.EvidenceID}
	denials := []FixtureDenial{}
	for _, trial := range []struct {
		reason, target, action string
		revoked                bool
	}{{"competing_target", "in-progress", fixtureAction, false}, {"revoked_grant", target, fixtureAction, true}, {"non_exact_action", target, "fixture.other", false}} {
		g := grant
		g.Revoked = trial.revoked
		if _, err := applyFixtureGrant(ctx, p, store, board, &g, origin, "denied-"+trial.reason, trial.target, trial.action); !errors.Is(err, ErrFixtureGrantDenied) {
			return FixturePOCReport{}, fmt.Errorf("fixture POC: expected denial")
		}
		denials = append(denials, FixtureDenial{Reason: trial.reason, TargetID: trial.target, Action: trial.action})
	}
	stale := grant
	staleOrigin := origin
	staleOrigin.EvidenceID = "stale-evidence"
	if _, err := applyFixtureGrant(ctx, p, store, board, &stale, staleOrigin, "denied-stale", target, fixtureAction); !errors.Is(err, ErrFixtureGrantDenied) {
		return FixturePOCReport{}, fmt.Errorf("fixture POC: expected stale evidence denial")
	}
	denials = append(denials, FixtureDenial{Reason: "stale_evidence", TargetID: target, Action: fixtureAction})
	board, err = loadFixtureBoard(ctx, store.Durable, "fixture-operation-1")
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
	return FixturePOCReport{Decision: decision, SelectedAction: fixtureAction, Grant: grant, Readback: readback, Before: origin.Tasks, After: after, Denials: denials}, nil
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

type fixtureApplyHooks struct {
	BeforeOperationRecord func() error
	AfterEffect           func() error
	BeforeFinalRecord     func() error
}

func applyFixtureGrant(ctx context.Context, p *Plugin, store governor.Store, board *fixtureBoard, grant *FixtureGrant, origin governor.Observation, operationID, target, action string) (FixtureReadback, error) {
	return applyFixtureGrantWithHooks(ctx, p, store, board, grant, origin, operationID, target, action, fixtureApplyHooks{})
}

func applyFixtureGrantWithHooks(ctx context.Context, p *Plugin, store governor.Store, board *fixtureBoard, grant *FixtureGrant, origin governor.Observation, operationID, target, action string, hooks fixtureApplyHooks) (FixtureReadback, error) {
	if grant == nil || grant.Revoked || grant.TargetID != target || grant.Action != action || grant.EvidenceID != origin.EvidenceID {
		return FixtureReadback{}, ErrFixtureGrantDenied
	}
	if prior, found, err := store.Durable.GetRecord(ctx, origin.WorkspaceID, operationID); err != nil {
		return FixtureReadback{}, err
	} else if found {
		savedTarget, tok := prior.Body["target"].(string)
		savedAction, aok := prior.Body["action"].(string)
		savedGrant, gok := prior.Body["grant"].(string)
		savedEvidence, eok := prior.Body["evidence"].(string)
		verified, vok := prior.Body["verified"].(bool)
		phase, pok := prior.Body["phase"].(string)
		if !tok || !aok || !gok || !eok || !vok || !pok || savedTarget != target || savedAction != action || savedGrant != grant.ID || savedEvidence != origin.EvidenceID {
			return FixtureReadback{}, ErrFixtureGrantDenied
		}
		if verified {
			state, sok := prior.Body["state"].(string)
			receipt, rok := prior.Body["receipt"].(string)
			if !sok || !rok {
				return FixtureReadback{}, ErrFixtureOutcomeUnknown
			}
			ready, err := fixtureBoardState(ctx, board, target)
			if err != nil {
				return FixtureReadback{}, err
			}
			if !ready {
				return FixtureReadback{}, ErrFixtureOutcomeUnknown
			}
			return FixtureReadback{OperationID: operationID, TaskID: target, State: state, Receipt: receipt, Replay: true}, nil
		}
		if phase != "pending" {
			return FixtureReadback{}, ErrFixtureOutcomeUnknown
		}
		ready, err := fixtureBoardState(ctx, board, target)
		if err != nil {
			return FixtureReadback{}, err
		}
		if ready {
			return finishFixtureGrant(ctx, p, store, board, origin, operationID, target, action, grant.ID, true, hooks)
		}
	} else {
		// Bind a fresh intent to the whole originating observation. A later
		// observation with the same evidence token is still a superseding denial.
		if err := store.ValidateCurrentObservation(ctx, origin); err != nil {
			return FixtureReadback{}, ErrFixtureGrantDenied
		}
		if hooks.BeforeOperationRecord != nil {
			if err := hooks.BeforeOperationRecord(); err != nil {
				return FixtureReadback{}, err
			}
		}
		// Commit the governor's immutable acceptance before creating fixture
		// intent. This closes the validate-to-intent gap: a superseding
		// observation is rejected without a board operation record or effect.
		if err := recordFixtureRecovery(ctx, store, origin, operationID, target, action, grant.ID); err != nil {
			return FixtureReadback{}, fmt.Errorf("%w: %v", ErrFixtureGrantDenied, err)
		}
		projection := map[string]any{"phase": "pending", "target": target, "action": action, "grant": grant.ID, "evidence": origin.EvidenceID, "operation": operationID, "verified": false}
		if _, err := store.Durable.AppendAdd(ctx, origin.WorkspaceID, 0, operationID, durablestate.KindDoneReceipt, projection, durablestate.StorageInline); err != nil {
			return FixtureReadback{}, err
		}
	}
	// A pending projection can be recovered after interruption. Its accepted
	// governor receipt is the durable authorization for the one fixture update.
	if err := recordFixtureRecovery(ctx, store, origin, operationID, target, action, grant.ID); err != nil {
		return FixtureReadback{}, fmt.Errorf("%w: %v", ErrFixtureGrantDenied, err)
	}
	grant.Used = true
	if err := board.Apply(target, action); err != nil {
		return FixtureReadback{}, err
	}
	if hooks.AfterEffect != nil {
		if err := hooks.AfterEffect(); err != nil {
			return FixtureReadback{}, err
		}
	}
	return finishFixtureGrant(ctx, p, store, board, origin, operationID, target, action, grant.ID, false, hooks)
}

func fixtureBoardState(ctx context.Context, board *fixtureBoard, target string) (bool, error) {
	tasks, err := board.Read(ctx)
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.ID == target {
			return task.State == "Ready", nil
		}
	}
	return false, ErrFixtureGrantDenied
}

func finishFixtureGrant(ctx context.Context, p *Plugin, store governor.Store, board *fixtureBoard, origin governor.Observation, operationID, target, action, grantID string, replay bool, hooks fixtureApplyHooks) (FixtureReadback, error) {
	recovery := fixtureRecovery(origin, operationID, target, action, grantID)
	if err := recordFixtureRecovery(ctx, store, origin, operationID, target, action, grantID); err != nil {
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
	if hooks.BeforeFinalRecord != nil {
		if err := hooks.BeforeFinalRecord(); err != nil {
			return FixtureReadback{}, err
		}
	}
	readback := FixtureReadback{OperationID: operationID, TaskID: target, State: "Ready", Receipt: receipt.EvidenceID, Replay: replay}
	projection := map[string]any{"phase": "verified", "state": "Ready", "receipt": receipt.EvidenceID, "target": target, "action": action, "grant": grantID, "evidence": origin.EvidenceID, "operation": operationID, "verified": true}
	_, err := store.Durable.AppendUpdate(ctx, origin.WorkspaceID, 0, operationID, projection, durablestate.StorageInline)
	if err != nil {
		return FixtureReadback{}, err
	}
	return readback, nil
}

func fixtureRecovery(origin governor.Observation, operationID, target, action, grantID string) governor.SolRecovery {
	return governor.SolRecovery{IncidentID: "fixture-incident", EventID: origin.EventID, EvidenceID: origin.EvidenceID, RequestID: operationID, ReceiptID: operationID + "/accepted", ProposedAction: action, ExpectedEffect: "task becomes Ready", ActualModel: governor.TierSol, ActualModelReceipt: grantID, Status: "decision_accepted", Accepted: true, StrategyVersion: origin.StrategyVersion, PlanVersion: origin.PlanVersion, CompletedAt: origin.ObservedAt, EffectDueAt: origin.ObservedAt.Add(time.Hour), AffectedTaskIDs: []string{target}}
}

func recordFixtureRecovery(ctx context.Context, store governor.Store, origin governor.Observation, operationID, target, action, grantID string) error {
	return store.RecordSolRecovery(ctx, 0, origin.WorkspaceID, fixtureRecovery(origin, operationID, target, action, grantID))
}

func fixtureObservation(event, evidence string, observed time.Time, targetState string) governor.Observation {
	return governor.Observation{SchemaVersion: governor.SchemaVersion, WorkspaceID: "fixture-workspace", ObservedAt: observed, EventID: event, EvidenceID: evidence, Provenance: "stage-0-fixture", Complete: true, StrategyVersion: 1, PlanVersion: 1, Tasks: []governor.Task{
		{ID: "done-dependency", State: "Done", Head: "fixture-head", PlanVersion: 1},
		{ID: "blocked-target", State: targetState, Dependencies: []string{"done-dependency"}, BlockerReason: map[bool]string{true: "fixture dependency cleared"}[targetState == "Blocked"], Head: "fixture-head", PlanVersion: 1},
		{ID: "in-progress", State: "InProgress", Head: "fixture-head", PlanVersion: 1},
	}}
}
