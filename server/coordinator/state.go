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
	stateVersion   = 2
	MaxReports     = 200
	MaxCycleLogs   = 200
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

type CoordinatorState struct {
	ActiveFlags   []ActiveFlag                    `json:"active_flags"`
	TaskSnapshots map[string]TaskActivitySnapshot `json:"task_snapshots"`
	Degradations  []string                        `json:"degradations"`
	LastReportAt  string                          `json:"last_report_at,omitempty"`
	CycleLogs     []CycleLog                      `json:"cycle_logs"`
}

type PublishedState struct {
	ActiveFlags   []ActiveFlag                    `json:"active_flags"`
	TaskSnapshots map[string]TaskActivitySnapshot `json:"task_snapshots"`
	Degradations  []string                        `json:"degradations"`
	CycleLog      *CycleLog                       `json:"cycle_log,omitempty"`
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
	return workspaceDocument{Version: stateVersion, State: CoordinatorState{TaskSnapshots: map[string]TaskActivitySnapshot{}}}
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
		doc.Version = stateVersion
		if doc.State.TaskSnapshots == nil {
			doc.State.TaskSnapshots = map[string]TaskActivitySnapshot{}
		}
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
	return doc, nil
}

func (p *Plugin) saveDocument(ctx context.Context, workspaceID string, doc workspaceDocument) error {
	doc.Version = stateVersion
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
	if err := p.saveDocument(ctx, workspaceID, doc); err != nil {
		return workspaceDocument{}, err
	}
	return doc, nil
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
