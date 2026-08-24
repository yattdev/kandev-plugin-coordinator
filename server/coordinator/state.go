package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	stateKeyV2     = "coordinator_state_v2"
	legacyStateKey = "coordinator_state"
	stateVersion   = 3
	MaxReports     = 200
	MaxCycleLogs   = 200
	MaxRuns        = 100
	MaxFollowUps   = 200
	MaxInboxItems  = 200
	cycleLogMaxAge = 7 * 24 * time.Hour
)

type ActiveFlag struct {
	TaskID    string `json:"task_id"`
	Reason    string `json:"reason"`
	FlaggedAt string `json:"flagged_at"`
}

type TaskActivitySnapshot struct {
	TaskID         string `json:"task_id"`
	WorkflowStepID string `json:"workflow_step_id,omitempty"`
	LastActivityAt string `json:"last_activity_at,omitempty"`
	LastCheckedAt  string `json:"last_checked_at,omitempty"`
	Classification string `json:"classification,omitempty"`
}

type CycleLog struct {
	At      string `json:"at"`
	Summary string `json:"summary"`
}

// CoordinatorIdentity is deliberately logical rather than execution-backed.
// A host may replace a task or session without changing the Workspace
// Coordinator's policy, history, inbox, or follow-up obligations.
type CoordinatorIdentity struct {
	LogicalKey string `json:"logical_key"`
}

type CoordinatorRun struct {
	ID            string `json:"id"`
	OccurrenceKey string `json:"occurrence_key,omitempty"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
}

// FollowUp is a durable request/receipt ledger. It describes an obligation,
// not an authority grant: agents and host APIs remain responsible for any
// later action.
type FollowUp struct {
	ID               string `json:"id"`
	TargetTaskID     string `json:"target_task_id,omitempty"`
	TargetSessionID  string `json:"target_session_id,omitempty"`
	Request          string `json:"request"`
	ExpectedEvidence string `json:"expected_evidence"`
	SentAt           string `json:"sent_at"`
	DueAt            string `json:"due_at,omitempty"`
	AttemptCount     int    `json:"attempt_count"`
	Status           string `json:"status"`
	Fallback         string `json:"fallback,omitempty"`
	LastObserved     string `json:"last_observed,omitempty"`
}

type InboxItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	TaskID    string `json:"task_id,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	CreatedAt string `json:"created_at"`
	Status    string `json:"status"`
}

type CapabilityState struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// CapabilityStates makes an older host's missing V1 seams visible without a
// hidden fallback. Principal, inbox, and Automation delivery remain host
// contracts; the plugin never synthesizes them from a backing task.
type CapabilityStates struct {
	Principal   CapabilityState `json:"principal"`
	Inbox       CapabilityState `json:"inbox"`
	Automations CapabilityState `json:"automations"`
	Relations   CapabilityState `json:"relations"`
}

// AutomationBinding is operator-selected workspace configuration. It stores
// only a descriptor reference; schedules, event definitions, and secrets stay
// in Kandev Automations.
type AutomationBinding struct {
	AutomationID string `json:"automation_id"`
	Name         string `json:"name"`
	BoundAt      string `json:"bound_at"`
}

type CoordinatorState struct {
	Identity           CoordinatorIdentity             `json:"identity"`
	ActiveFlags        []ActiveFlag                    `json:"active_flags"`
	TaskSnapshots      map[string]TaskActivitySnapshot `json:"task_snapshots"`
	Degradations       []string                        `json:"degradations"`
	LastReportAt       string                          `json:"last_report_at,omitempty"`
	CycleLogs          []CycleLog                      `json:"cycle_logs"`
	Runs               []CoordinatorRun                `json:"runs"`
	FollowUps          []FollowUp                      `json:"follow_ups"`
	Inbox              []InboxItem                     `json:"inbox"`
	Capabilities       CapabilityStates                `json:"capabilities"`
	AutomationBindings []AutomationBinding             `json:"automation_bindings"`
}

type PublishedState struct {
	ActiveFlags   []ActiveFlag                    `json:"active_flags"`
	TaskSnapshots map[string]TaskActivitySnapshot `json:"task_snapshots"`
	Degradations  []string                        `json:"degradations"`
	CycleLog      *CycleLog                       `json:"cycle_log,omitempty"`
	Runs          []CoordinatorRun                `json:"runs"`
	FollowUps     []FollowUp                      `json:"follow_ups"`
	Inbox         []InboxItem                     `json:"inbox"`
}

type workspaceDocument struct {
	Version int              `json:"version"`
	State   CoordinatorState `json:"state"`
	Reports []ReportArtifact `json:"reports"`
}

type workspaceLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (l *workspaceLocks) forWorkspace(workspaceID string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.locks == nil {
		l.locks = make(map[string]*sync.Mutex)
	}
	if l.locks[workspaceID] == nil {
		l.locks[workspaceID] = &sync.Mutex{}
	}
	return l.locks[workspaceID]
}

func emptyDocument() workspaceDocument {
	return workspaceDocument{Version: stateVersion, State: CoordinatorState{
		Identity:      CoordinatorIdentity{LogicalKey: ConversationKey},
		TaskSnapshots: map[string]TaskActivitySnapshot{},
		Capabilities:  hostCapabilityStates(),
	}}
}

func (p *Plugin) loadDocument(ctx context.Context, workspaceID string) (workspaceDocument, error) {
	value, found, err := p.Host().GetState(ctx, "workspace", workspaceID, stateKeyV2)
	if err != nil {
		return workspaceDocument{}, err
	}
	if found {
		doc := emptyDocument()
		if err := mapInto(value, &doc); err != nil {
			return workspaceDocument{}, fmt.Errorf("decode coordinator state: %w", err)
		}
		normalizeDocument(&doc)
		return doc, nil
	}
	return p.migrateLegacyDocument(ctx, workspaceID)
}

func (p *Plugin) migrateLegacyDocument(ctx context.Context, workspaceID string) (workspaceDocument, error) {
	doc := emptyDocument()
	legacy, found, err := p.Host().GetState(ctx, "workspace", workspaceID, legacyStateKey)
	if err != nil || !found {
		return doc, err
	}
	report, _ := legacy["report"].(string)
	at, _ := legacy["last_report_at"].(string)
	if strings.TrimSpace(report) != "" {
		if at == "" {
			at = p.now().UTC().Format(time.RFC3339Nano)
		}
		doc.State.LastReportAt = at
		doc.Reports = []ReportArtifact{{ID: "legacy-report", Type: ReportDaily, Title: "Migrated coordinator report", Body: report, CreatedAt: at}}
	}
	normalizeDocument(&doc)
	return doc, nil
}

func (p *Plugin) saveDocument(ctx context.Context, workspaceID string, doc workspaceDocument) error {
	normalizeDocument(&doc)
	value, err := structMap(doc)
	if err != nil {
		return err
	}
	return p.Host().SetState(ctx, "workspace", workspaceID, stateKeyV2, value)
}

func (p *Plugin) readState(ctx context.Context, workspaceID string) (CoordinatorState, error) {
	lock := p.locks.forWorkspace(workspaceID)
	lock.Lock()
	defer lock.Unlock()
	doc, err := p.loadDocument(ctx, workspaceID)
	return doc.State, err
}

func (p *Plugin) updateDocument(ctx context.Context, workspaceID string, update func(*workspaceDocument) error) (workspaceDocument, error) {
	lock := p.locks.forWorkspace(workspaceID)
	lock.Lock()
	defer lock.Unlock()
	doc, err := p.loadDocument(ctx, workspaceID)
	if err != nil {
		return workspaceDocument{}, err
	}
	if err := update(&doc); err != nil {
		return workspaceDocument{}, err
	}
	compactCycleLogs(&doc.State, p.now())
	if len(doc.State.CycleLogs) > MaxCycleLogs {
		doc.State.CycleLogs = doc.State.CycleLogs[:MaxCycleLogs]
	}
	if len(doc.Reports) > MaxReports {
		doc.Reports = doc.Reports[:MaxReports]
	}
	if len(doc.State.Runs) > MaxRuns {
		doc.State.Runs = doc.State.Runs[:MaxRuns]
	}
	if len(doc.State.FollowUps) > MaxFollowUps {
		doc.State.FollowUps = doc.State.FollowUps[:MaxFollowUps]
	}
	if len(doc.State.Inbox) > MaxInboxItems {
		doc.State.Inbox = doc.State.Inbox[:MaxInboxItems]
	}
	if err := p.saveDocument(ctx, workspaceID, doc); err != nil {
		return workspaceDocument{}, err
	}
	return doc, nil
}

func normalizeDocument(doc *workspaceDocument) {
	doc.Version = stateVersion
	if doc.State.Identity.LogicalKey == "" {
		doc.State.Identity.LogicalKey = ConversationKey
	}
	if doc.State.TaskSnapshots == nil {
		doc.State.TaskSnapshots = map[string]TaskActivitySnapshot{}
	}
	if doc.State.Capabilities.Principal.Status == "" {
		doc.State.Capabilities = hostCapabilityStates()
	}
}

func hostCapabilityStates() CapabilityStates {
	return CapabilityStates{
		Principal:   CapabilityState{Status: "unavailable", Reason: "This host does not expose a Coordinator principal API."},
		Inbox:       CapabilityState{Status: "unavailable", Reason: "This host does not expose a Coordinator inbox API."},
		Automations: CapabilityState{Status: "degraded", Reason: "Automation delivery can use bound IDs; creation and schedule edits remain in Kandev settings."},
		Relations:   CapabilityState{Status: "available"},
	}
}

func compactCycleLogs(state *CoordinatorState, now time.Time) {
	cutoff := now.Add(-cycleLogMaxAge)
	weekly := map[string]int{}
	recent := make([]CycleLog, 0, len(state.CycleLogs))
	for _, entry := range state.CycleLogs {
		parsed, err := time.Parse(time.RFC3339Nano, entry.At)
		if err != nil || !parsed.Before(cutoff) {
			recent = append(recent, entry)
			continue
		}
		year, week := parsed.ISOWeek()
		weekly[fmt.Sprintf("%04d-W%02d", year, week)]++
	}
	keys := make([]string, 0, len(weekly))
	for key := range weekly {
		keys = append(keys, key)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	for _, key := range keys {
		recent = append(recent, CycleLog{At: now.UTC().Format(time.RFC3339Nano), Summary: fmt.Sprintf("%s: %d older cycle logs compacted", key, weekly[key])})
	}
	state.CycleLogs = recent
}

func structMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func mapInto(value map[string]any, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}
