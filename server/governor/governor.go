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
var ErrDigestTooSmall = errors.New("shadow governor: digest limit cannot represent required attention")
var errNoMutation = errors.New("shadow governor: no state mutation")

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
	// Activity is deliberately separate from progress.  Callers may record
	// moves and messages here, but only verified outcome evidence advances
	// LastProgress.
	ActivityAt       time.Time     `json:"activity_at,omitempty"`
	EvidenceDueAt    time.Time     `json:"evidence_due_at,omitempty"`
	VerifiedEvidence string        `json:"verified_evidence,omitempty"`
	ExternalWait     *ExternalWait `json:"external_wait,omitempty"`
	ExecutorTier     Tier          `json:"executor_tier,omitempty"`
}
type ExternalWait struct {
	Source, Owner, Trigger, Fallback string
	ExpiresAt                        time.Time `json:"expires_at"`
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
	Last           Observation            `json:"last"`
	StrategyAt     time.Time              `json:"strategy_at"`
	Events         []string               `json:"events"`
	Contracts      map[string]Contract    `json:"contracts,omitempty"`
	BaselineAt     time.Time              `json:"baseline_at,omitempty"`
	Results        map[string]Result      `json:"results,omitempty"`
	EventBodies    map[string]string      `json:"event_bodies,omitempty"`
	Observations   map[string]Observation `json:"observations,omitempty"`
	ReviewBaseline Observation            `json:"review_baseline,omitempty"`
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
	bodyHash, _ := json.Marshal(in)
	if in.ObservedAt.After(now.Add(5 * time.Minute)) {
		return Result{}, fmt.Errorf("shadow governor: future observation")
	}
	var out Result
	err := s.transform(ctx, fence, in.WorkspaceID, func(st *state) error {
		if prior, ok := st.EventBodies[in.EventID]; ok {
			if prior != string(bodyHash) {
				return fmt.Errorf("shadow governor: duplicate event payload conflict")
			}
			out = st.Results[in.EventID]
			out.Duplicate = true
			return errNoMutation
		}
		if !st.Last.ObservedAt.IsZero() && in.ObservedAt.Before(st.Last.ObservedAt) {
			return fmt.Errorf("shadow governor: stale observation")
		}
		var evalErr error
		out, evalErr = evaluate(*st, in, cfg)
		if evalErr != nil {
			return evalErr
		}
		st.Last = in
		st.Events = append(st.Events, in.EventID)
		if st.Results == nil {
			st.Results = map[string]Result{}
		}
		if st.EventBodies == nil {
			st.EventBodies = map[string]string{}
		}
		if st.Observations == nil {
			st.Observations = map[string]Observation{}
		}
		st.Results[in.EventID] = out
		st.EventBodies[in.EventID] = string(bodyHash)
		st.Observations[in.EventID] = in
		if len(st.Events) > 128 {
			stale := st.Events[0]
			st.Events = st.Events[1:]
			delete(st.Results, stale)
			delete(st.EventBodies, stale)
			delete(st.Observations, stale)
		}
		if st.BaselineAt.IsZero() && in.Complete {
			st.BaselineAt = in.ObservedAt
		}
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("shadow governor checkpoint: %w", err)
	}
	return out, nil
}

// PutContract is a durable expected-version compare-and-swap. It is only a
// shadow-state operation: callers still must validate the returned contract at
// their actual Host action boundary.
func (s Store) PutContract(ctx context.Context, fence int64, expectedVersion int, contract Contract) (Contract, error) {
	if err := validateContractShape(contract); err != nil {
		return Contract{}, fmt.Errorf("shadow governor: incomplete task contract")
	}
	err := s.transform(ctx, fence, contract.WorkspaceID, func(st *state) error {
		if st.Contracts == nil {
			st.Contracts = map[string]Contract{}
		}
		current, exists := st.Contracts[contract.TaskID]
		if (exists && current.Version != expectedVersion) || (!exists && expectedVersion != 0) {
			return ErrStaleContract
		}
		contract.Version = expectedVersion + 1
		st.Contracts[contract.TaskID] = contract
		return nil
	})
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

// transform applies fn to precisely the record that is later compared and
// swapped.  A conflict is retried from a newly decoded record; callers never
// write a body derived from a stale read.
func (s Store) transform(ctx context.Context, fence int64, workspace string, fn func(*state) error) error {
	for attempt := 0; attempt != 8; attempt++ {
		rec, found, err := s.Durable.GetRecord(ctx, workspace, stateRecordID)
		if err != nil {
			return err
		}
		st := state{}
		if found {
			raw, err := json.Marshal(rec.Body)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &st); err != nil {
				return err
			}
		}
		if err := fn(&st); errors.Is(err, errNoMutation) {
			return nil
		} else if err != nil {
			return err
		}
		body, err := toBody(st)
		if err != nil {
			return err
		}
		if !found {
			_, err = s.Durable.AppendAdd(ctx, workspace, fence, stateRecordID, durablestate.KindShadowGovernor, body, durablestate.StorageInline)
		} else {
			_, err = s.Durable.CompareAndSwapRecord(ctx, workspace, fence, stateRecordID, rec.SHA256, body, durablestate.StorageInline)
		}
		if err == nil {
			return nil
		}
		if errors.Is(err, durablestate.ErrRecordConflict) || errors.Is(err, durablestate.ErrRecordAlreadyExists) {
			continue
		}
		return err
	}
	return fmt.Errorf("shadow governor: checkpoint contention exceeded retry limit")
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
	if len(in.WorkspaceID) > 256 || len(in.EventID) > 256 || len(in.Provenance) > 256 {
		return fmt.Errorf("shadow governor: identifier limit exceeded")
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
func evaluate(old state, in Observation, c Config) (Result, error) {
	d := Digest{SchemaVersion: SchemaVersion, WorkspaceID: in.WorkspaceID, Complete: in.Complete, Provenance: in.Provenance}
	comparison := old.Last
	if !old.ReviewBaseline.ObservedAt.IsZero() {
		comparison = old.ReviewBaseline
	}
	byID := map[string]Task{}
	for _, t := range comparison.Tasks {
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
		return result(d, []string{"unknown_evidence"}), nil
	}
	blocked := 0
	overdue := 0
	for _, t := range in.Tasks {
		if !t.EvidenceDueAt.IsZero() && !t.LastProgress.After(t.EvidenceDueAt) && in.ObservedAt.After(t.EvidenceDueAt) && !provenExternalWait(t.ExternalWait, in.ObservedAt) {
			overdue++
			d.Attention = append(d.Attention, Attention{TaskID: t.ID, Reason: "outcome_overdue", Tier: TierSol})
		}
		if t.BlockerReason != "" {
			blocked++
			tier := TierSol
			reason := "blocked"
			if strings.Contains(strings.ToLower(t.BlockerReason), "contradict") || strings.Contains(strings.ToLower(t.BlockerReason), "plan_invalid") {
				tier = TierAstra
				reason = "conflict"
			}
			d.Attention = append(d.Attention, Attention{TaskID: t.ID, Reason: reason, Tier: tier})
		} else if t.ExecutorTier == TierLuna || t.ExecutorTier == TierTerra {
			d.Attention = append(d.Attention, Attention{TaskID: t.ID, Reason: "next_action_ready", Tier: t.ExecutorTier})
		}
	}
	if blocked > 1 {
		d.Attention = append(d.Attention, Attention{Reason: "multiple_blocked", Tier: TierAstra})
	}
	if overdue >= 2 && old.Last.Complete && old.LastHasOverdue() {
		d.Attention = append(d.Attention, Attention{Reason: "overdue_cohort", Tier: TierAstra})
	}
	if !old.StrategyAt.IsZero() && in.ObservedAt.Before(old.StrategyAt) {
		d.Attention = append(d.Attention, Attention{Reason: "clock_rollback", Tier: TierAstra})
	}
	if !old.BaselineAt.IsZero() && len(in.Tasks) > 0 && in.ObservedAt.Sub(old.StrategyAtOrBaseline()) >= c.Watchdog {
		d.Attention = append(d.Attention, Attention{Reason: "active_watchdog", Tier: TierAstra})
	}
	r := result(d, nil)
	if err := boundDigest(&r.Digest, c.MaxDigestBytes); err != nil {
		return Result{}, err
	}
	r = result(r.Digest, r.Reasons)
	return r, nil
}
func provenExternalWait(w *ExternalWait, at time.Time) bool {
	return w != nil && w.Source != "" && w.Owner != "" && w.Trigger != "" && w.Fallback != "" && w.ExpiresAt.After(at)
}
func (s state) LastHasOverdue() bool {
	for _, t := range s.Last.Tasks {
		if !t.EvidenceDueAt.IsZero() && !t.LastProgress.After(t.EvidenceDueAt) && s.Last.ObservedAt.After(t.EvidenceDueAt) && !provenExternalWait(t.ExternalWait, s.Last.ObservedAt) {
			return true
		}
	}
	return false
}

func boundDigest(d *Digest, max int) error {
	if max < 1 {
		return ErrDigestTooSmall
	}
	for len(d.Attention) > 1 {
		raw, _ := json.Marshal(d)
		if len(raw) <= max {
			return nil
		}
		d.Attention = d.Attention[:len(d.Attention)-1]
		d.Omitted++
	}
	raw, _ := json.Marshal(d)
	if len(raw) > max {
		return ErrDigestTooSmall
	}
	return nil
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
	return s.transform(ctx, fence, workspace, func(st *state) error {
		obs, ok := st.Observations[eventID]
		if !ok {
			return fmt.Errorf("shadow governor: unknown review event")
		}
		if !obs.Complete || at.Before(obs.ObservedAt) {
			return fmt.Errorf("shadow governor: ineligible review event")
		}
		if at.Before(st.StrategyAt) {
			return ErrStaleContract
		}
		st.StrategyAt = at
		st.BaselineAt = obs.ObservedAt
		st.ReviewBaseline = obs
		return nil
	})
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
	if validateContractShape(c) != nil || c.Version < 1 || c.WorkspaceID != workspace || c.TaskID != task || c.Head != head || c.Generation != generation || !c.ExpiresAt.After(now) {
		return ErrStaleContract
	}
	return nil
}

// ValidateCurrentContract checks the durable contract and the caller's current
// strategy/plan versions together.  It is intentionally advisory: an embedding
// Host must repeat this guard at its own action boundary.
func (s Store) ValidateCurrentContract(ctx context.Context, workspace, task, head, generation string, strategyVersion, planVersion int, now time.Time) (Contract, error) {
	st, found, err := s.load(ctx, workspace)
	if err != nil || !found {
		return Contract{}, ErrStaleContract
	}
	c, ok := st.Contracts[task]
	if !ok || c.StrategyVersion != strategyVersion || c.PlanVersion != planVersion || ValidateContract(c, workspace, task, head, generation, now) != nil {
		return Contract{}, ErrStaleContract
	}
	return c, nil
}
func validateContractShape(c Contract) error {
	if c.Version < 0 || c.StrategyVersion < 1 || c.PlanVersion < 1 || c.WorkspaceID == "" || c.TaskID == "" || c.Head == "" || c.Generation == "" || c.Goal == "" || c.NextAction == "" || c.ExpiresAt.IsZero() {
		return ErrStaleContract
	}
	switch c.ExecutorTier {
	case TierLuna, TierTerra, TierSol, TierAstra:
	default:
		return ErrStaleContract
	}
	if len(c.AllowedActions) == 0 || len(c.CompletionConditions) == 0 {
		return ErrStaleContract
	}
	seen := map[string]bool{}
	for _, a := range c.AllowedActions {
		if a == "" {
			return ErrStaleContract
		}
		seen[a] = true
	}
	for _, a := range c.ProhibitedActions {
		if a == "" || seen[a] {
			return ErrStaleContract
		}
	}
	return nil
}
