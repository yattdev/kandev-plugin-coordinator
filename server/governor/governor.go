// Package governor evaluates supplied board snapshots without reading or
// mutating the Host. It is a shadow pilot: every result is a recommendation.
package governor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"kandev-plugin-coordinator/server/durablestate"
)

const SchemaVersion = "shadow-governor/v1"
const stateRecordID = "shadow-governor/v1"

var ErrStaleContract = errors.New("shadow governor: stale or mismatched contract")

type Tier string

const (
	TierNone  Tier = "none"
	TierLuna  Tier = "luna"
	TierTerra Tier = "terra"
	TierSol   Tier = "sol"
	TierAstra Tier = "astra"
)

type Task struct {
	ID            string    `json:"id"`
	Lane          string    `json:"lane"`
	State         string    `json:"state"`
	Owner         string    `json:"owner,omitempty"`
	Dependencies  []string  `json:"dependencies,omitempty"`
	BlockerReason string    `json:"blocker_reason,omitempty"`
	Head          string    `json:"head,omitempty"`
	PlanVersion   int       `json:"plan_version,omitempty"`
	LastProgress  time.Time `json:"last_progress,omitempty"`
	LatestResult  string    `json:"latest_result,omitempty"`
}
type Observation struct {
	SchemaVersion string    `json:"schema_version"`
	WorkspaceID   string    `json:"workspace_id"`
	ObservedAt    time.Time `json:"observed_at"`
	EventID       string    `json:"event_id"`
	Provenance    string    `json:"provenance"`
	Complete      bool      `json:"complete"`
	Tasks         []Task    `json:"tasks"`
}
type Attention struct {
	TaskID string `json:"task_id,omitempty"`
	Reason string `json:"reason"`
	Tier   Tier   `json:"tier"`
}
type Digest struct {
	SchemaVersion string      `json:"schema_version"`
	WorkspaceID   string      `json:"workspace_id"`
	Complete      bool        `json:"complete"`
	Provenance    string      `json:"provenance"`
	Changed       int         `json:"changed"`
	Omitted       int         `json:"omitted"`
	Attention     []Attention `json:"attention"`
}
type Result struct {
	Duplicate           bool     `json:"duplicate"`
	Decision            Tier     `json:"decision"`
	Reasons             []string `json:"reasons"`
	Digest              Digest   `json:"digest"`
	LiveBoardCollection string   `json:"live_board_collection"`
}
type Config struct {
	Watchdog       time.Duration
	MaxTasks       int
	MaxDigestBytes int
}

func (c Config) normalized() Config {
	if c.Watchdog == 0 {
		c.Watchdog = 3 * time.Hour
	}
	if c.Watchdog < time.Hour {
		c.Watchdog = time.Hour
	}
	if c.Watchdog > 24*time.Hour {
		c.Watchdog = 24 * time.Hour
	}
	if c.MaxTasks == 0 {
		c.MaxTasks = 100
	}
	if c.MaxTasks < 1 {
		c.MaxTasks = 1
	}
	if c.MaxDigestBytes == 0 {
		c.MaxDigestBytes = 8192
	}
	return c
}

type state struct {
	Last        Observation         `json:"last"`
	StrategyAt  time.Time           `json:"strategy_at"`
	Events      []string            `json:"events"`
	Contracts   map[string]Contract `json:"contracts,omitempty"`
	BaselineAt  time.Time           `json:"baseline_at,omitempty"`
	Results     map[string]Result   `json:"results,omitempty"`
	EventBodies map[string]string   `json:"event_bodies,omitempty"`
}
type Store struct {
	Durable *durablestate.Store
	Now     func() time.Time
	Config  Config
}

func (s Store) Observe(ctx context.Context, fence int64, in Observation) (Result, error) {
	cfg := s.Config.normalized()
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if err := validate(in, cfg); err != nil {
		return Result{}, err
	}
	st, found, err := s.load(ctx, in.WorkspaceID)
	if err != nil {
		return Result{}, err
	}
	bodyHash, _ := json.Marshal(in)
	for _, id := range st.Events {
		if id == in.EventID {
			if st.EventBodies[in.EventID] != string(bodyHash) {
				return Result{}, fmt.Errorf("shadow governor: duplicate event payload conflict")
			}
			r := st.Results[in.EventID]
			r.Duplicate = true
			return r, nil
		}
	}
	if !st.Last.ObservedAt.IsZero() && in.ObservedAt.Before(st.Last.ObservedAt) {
		return Result{}, fmt.Errorf("shadow governor: stale observation")
	}
	if in.ObservedAt.After(now.Add(5 * time.Minute)) {
		return Result{}, fmt.Errorf("shadow governor: future observation")
	}
	result := evaluate(st, in, cfg)
	st.Last = in
	st.Events = append(st.Events, in.EventID)
	if len(st.Events) > 128 {
		st.Events = st.Events[len(st.Events)-128:]
	}
	if st.BaselineAt.IsZero() && in.Complete {
		st.BaselineAt = in.ObservedAt
	}
	if st.Results == nil {
		st.Results = map[string]Result{}
	}
	if st.EventBodies == nil {
		st.EventBodies = map[string]string{}
	}
	st.Results[in.EventID] = result
	st.EventBodies[in.EventID] = string(bodyHash)
	body, err := toBody(st)
	if err != nil {
		return Result{}, err
	}
	if !found {
		_, err = s.Durable.AppendAdd(ctx, in.WorkspaceID, fence, stateRecordID, durablestate.KindShadowGovernor, body, durablestate.StorageInline)
	} else {
		_, err = s.Durable.AppendUpdate(ctx, in.WorkspaceID, fence, stateRecordID, body, durablestate.StorageInline)
	}
	if err != nil {
		return Result{}, fmt.Errorf("shadow governor checkpoint: %w", err)
	}
	return result, nil
}

// PutContract is a durable expected-version compare-and-swap. It is only a
// shadow-state operation: callers still must validate the returned contract at
// their actual Host action boundary.
func (s Store) PutContract(ctx context.Context, fence int64, expectedVersion int, contract Contract) (Contract, error) {
	if contract.Version < 0 || contract.WorkspaceID == "" || contract.TaskID == "" || contract.Head == "" || contract.Generation == "" || contract.Goal == "" || contract.NextAction == "" || contract.ExpiresAt.IsZero() {
		return Contract{}, fmt.Errorf("shadow governor: incomplete task contract")
	}
	st, found, err := s.load(ctx, contract.WorkspaceID)
	if err != nil {
		return Contract{}, err
	}
	if st.Contracts == nil {
		st.Contracts = map[string]Contract{}
	}
	current, exists := st.Contracts[contract.TaskID]
	if exists && current.Version != expectedVersion {
		return Contract{}, ErrStaleContract
	}
	if !exists && expectedVersion != 0 {
		return Contract{}, ErrStaleContract
	}
	contract.Version = expectedVersion + 1
	st.Contracts[contract.TaskID] = contract
	body, err := toBody(st)
	if err != nil {
		return Contract{}, err
	}
	if !found {
		_, err = s.Durable.AppendAdd(ctx, contract.WorkspaceID, fence, stateRecordID, durablestate.KindShadowGovernor, body, durablestate.StorageInline)
	} else {
		_, err = s.Durable.AppendUpdate(ctx, contract.WorkspaceID, fence, stateRecordID, body, durablestate.StorageInline)
	}
	if err != nil {
		return Contract{}, fmt.Errorf("shadow governor contract checkpoint: %w", err)
	}
	return contract, nil
}
func (s Store) load(ctx context.Context, workspace string) (state, bool, error) {
	r, ok, err := s.Durable.GetRecord(ctx, workspace, stateRecordID)
	if err != nil || !ok {
		return state{}, ok, err
	}
	raw, err := json.Marshal(r.Body)
	if err != nil {
		return state{}, false, err
	}
	var v state
	err = json.Unmarshal(raw, &v)
	return v, true, err
}
func toBody(v state) (map[string]any, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	var out map[string]any
	e = json.Unmarshal(b, &out)
	return out, e
}
func validate(in Observation, c Config) error {
	if in.SchemaVersion != SchemaVersion || strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.EventID) == "" || strings.TrimSpace(in.Provenance) == "" || in.ObservedAt.IsZero() {
		return fmt.Errorf("shadow governor: schema_version, workspace_id, event_id, provenance, and observed_at are required")
	}
	if len(in.Tasks) > c.MaxTasks {
		return fmt.Errorf("shadow governor: task limit exceeded")
	}
	seen := map[string]bool{}
	for _, t := range in.Tasks {
		if t.ID == "" || seen[t.ID] {
			return fmt.Errorf("shadow governor: task IDs must be non-empty and unique")
		}
		seen[t.ID] = true
	}
	return nil
}
func evaluate(old state, in Observation, c Config) Result {
	d := Digest{SchemaVersion: SchemaVersion, WorkspaceID: in.WorkspaceID, Complete: in.Complete, Provenance: in.Provenance}
	byID := map[string]Task{}
	for _, t := range old.Last.Tasks {
		byID[t.ID] = t
	}
	for _, t := range in.Tasks {
		if prev, ok := byID[t.ID]; !ok || prev.State != t.State || prev.Lane != t.Lane || prev.Owner != t.Owner || prev.Head != t.Head || prev.PlanVersion != t.PlanVersion || !prev.LastProgress.Equal(t.LastProgress) || strings.Join(prev.Dependencies, ",") != strings.Join(t.Dependencies, ",") || prev.BlockerReason != t.BlockerReason || prev.LatestResult != t.LatestResult {
			d.Changed++
		}
	}
	for id := range byID {
		found := false
		for _, t := range in.Tasks {
			if t.ID == id {
				found = true
				break
			}
		}
		if !found {
			d.Changed++
		}
	}
	if !in.Complete {
		d.Attention = []Attention{{Reason: "incomplete_observation", Tier: TierAstra}}
		return result(d, []string{"unknown_evidence"})
	}
	blocked := 0
	for _, t := range in.Tasks {
		if t.BlockerReason != "" {
			blocked++
			tier := TierSol
			reason := "blocked"
			if strings.Contains(strings.ToLower(t.BlockerReason), "contradict") || strings.Contains(strings.ToLower(t.BlockerReason), "plan_invalid") {
				tier = TierAstra
				reason = "conflict"
			}
			d.Attention = append(d.Attention, Attention{TaskID: t.ID, Reason: reason, Tier: tier})
		}
	}
	if blocked > 1 {
		d.Attention = append(d.Attention, Attention{Reason: "multiple_blocked", Tier: TierAstra})
	}
	if !old.StrategyAt.IsZero() && in.ObservedAt.Before(old.StrategyAt) {
		d.Attention = append(d.Attention, Attention{Reason: "clock_rollback", Tier: TierAstra})
	}
	if !old.BaselineAt.IsZero() && len(in.Tasks) > 0 && in.ObservedAt.Sub(old.StrategyAtOrBaseline()) >= c.Watchdog {
		d.Attention = append(d.Attention, Attention{Reason: "active_watchdog", Tier: TierAstra})
	}
	r := result(d, nil)
	raw, _ := json.Marshal(r.Digest)
	if len(raw) > c.MaxDigestBytes {
		r.Decision = TierAstra
		r.Reasons = append(r.Reasons, "digest_expansion_required")
		r.Digest.Omitted = len(r.Digest.Attention)
		r.Digest.Attention = []Attention{{Reason: "digest_expansion_required", Tier: TierAstra}}
	}
	return r
}
func (s state) StrategyAtOrBaseline() time.Time {
	if !s.StrategyAt.IsZero() {
		return s.StrategyAt
	}
	return s.BaselineAt
}

// AcknowledgeStrategy is the only operation allowed to advance the strategic
// watermark after a successful external review receipt.
func (s Store) AcknowledgeStrategy(ctx context.Context, fence int64, workspace, eventID string, at time.Time) error {
	st, found, err := s.load(ctx, workspace)
	if err != nil || !found {
		return fmt.Errorf("shadow governor: missing checkpoint: %w", err)
	}
	if _, ok := st.Results[eventID]; !ok {
		return fmt.Errorf("shadow governor: unknown review event")
	}
	st.StrategyAt = at
	b, err := toBody(st)
	if err != nil {
		return err
	}
	r, ok, err := s.Durable.GetRecord(ctx, workspace, stateRecordID)
	if err != nil || !ok {
		return fmt.Errorf("shadow governor: checkpoint vanished")
	}
	_, err = s.Durable.CompareAndSwapRecord(ctx, workspace, fence, stateRecordID, r.SHA256, b, durablestate.StorageInline)
	return err
}
func result(d Digest, base []string) Result {
	sort.Slice(d.Attention, func(i, j int) bool {
		return d.Attention[i].TaskID+d.Attention[i].Reason < d.Attention[j].TaskID+d.Attention[j].Reason
	})
	tier := TierNone
	reasons := base
	for _, a := range d.Attention {
		reasons = append(reasons, a.Reason)
		if a.Tier == TierAstra {
			tier = TierAstra
		} else if tier != TierAstra && a.Tier == TierSol {
			tier = TierSol
		}
	}
	return Result{Decision: tier, Reasons: reasons, Digest: d, LiveBoardCollection: "unavailable: normalized snapshot supplied by caller"}
}

type Contract struct {
	Version              int       `json:"version"`
	StrategyVersion      int       `json:"strategy_version"`
	PlanVersion          int       `json:"plan_version"`
	WorkspaceID          string    `json:"workspace_id"`
	TaskID               string    `json:"task_id"`
	Goal                 string    `json:"goal"`
	NextAction           string    `json:"next_action"`
	ExecutorTier         Tier      `json:"executor_tier"`
	AllowedActions       []string  `json:"allowed_actions"`
	ProhibitedActions    []string  `json:"prohibited_actions"`
	CompletionConditions []string  `json:"completion_conditions"`
	EscalationConditions []string  `json:"escalation_conditions"`
	Head                 string    `json:"head"`
	Generation           string    `json:"generation"`
	ExpiresAt            time.Time `json:"expires_at"`
}

func ValidateContract(c Contract, workspace, task, head, generation string, now time.Time) error {
	if c.Version < 1 || c.WorkspaceID != workspace || c.TaskID != task || c.Head != head || c.Generation != generation || !c.ExpiresAt.After(now) {
		return ErrStaleContract
	}
	return nil
}
