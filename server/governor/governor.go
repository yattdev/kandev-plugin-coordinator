// Package governor evaluates supplied board snapshots without reading or
// mutating the Host. It is a shadow pilot: every result is a recommendation.
package governor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	ActivityAt          time.Time     `json:"activity_at,omitempty"`
	EvidenceDueAt       time.Time     `json:"evidence_due_at,omitempty"`
	VerifiedEvidence    string        `json:"verified_evidence,omitempty"`
	ProgressHead        string        `json:"progress_head,omitempty"`
	ProgressPlanVersion int           `json:"progress_plan_version,omitempty"`
	Verifier            string        `json:"verifier,omitempty"`
	Milestone           string        `json:"milestone,omitempty"`
	ExternalWait        *ExternalWait `json:"external_wait,omitempty"`
	ExecutorTier        Tier          `json:"executor_tier,omitempty"`
}
type ExternalWait struct {
	Source, Owner, Trigger, Fallback string
	ExpiresAt                        time.Time `json:"expires_at"`
}
type Observation struct {
	SchemaVersion   string    `json:"schema_version"`
	WorkspaceID     string    `json:"workspace_id"`
	ObservedAt      time.Time `json:"observed_at"`
	EventID         string    `json:"event_id"`
	Provenance      string    `json:"provenance"`
	Complete        bool      `json:"complete"`
	StrategyVersion int       `json:"strategy_version"`
	PlanVersion     int       `json:"plan_version"`
	EvidenceID      string    `json:"evidence_id"`
	Tasks           []Task    `json:"tasks"`
}

// StrategyReceipt is evidence of a successful Astra review.  It is separate
// from a helper's start/completion receipt so failures and unknown outcomes can
// never advance the strategic watermark.
type StrategyReceipt struct {
	EventID, RequestID, IncidentID, EvidenceID string
	Model                                      Tier
	DirectAstraPrimary                         bool
	Outcome                                    string
	Accepted                                   bool
	StrategyVersion, PlanVersion               int
	ExpectedEffect                             string
	EffectDueAt, CompletedAt                   time.Time
}
type Attention struct {
	TaskID string `json:"task_id,omitempty"`
	Reason string `json:"reason"`
	Tier   Tier   `json:"tier"`
}
type Digest struct {
	SchemaVersion       string      `json:"schema_version"`
	WorkspaceID         string      `json:"workspace_id"`
	Complete            bool        `json:"complete"`
	Provenance          string      `json:"provenance"`
	Changed             int         `json:"changed"`
	Omitted             int         `json:"omitted"`
	OmittedTaskIDs      []string    `json:"omitted_task_ids,omitempty"`
	OmittedContinuation string      `json:"omitted_continuation,omitempty"`
	Attention           []Attention `json:"attention"`
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
	OverdueTasks   map[string]bool        `json:"overdue_tasks,omitempty"`
	Recoveries     map[string]SolRecovery `json:"recoveries,omitempty"`
}
type SolRecovery struct {
	IncidentID, EventID, EvidenceID, RequestID, ProposedAction, ExpectedEffect, EffectEvidenceID, RecurrenceID string
	StrategyVersion, PlanVersion                                                                               int
	ActualModel                                                                                                Tier
	Status                                                                                                     string
	Accepted                                                                                                   bool
	CompletedAt, EffectDueAt                                                                                   time.Time
	AffectedTaskIDs                                                                                            []string
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
		st.OverdueTasks = actionableOverdue(in)
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
		if !t.LastProgress.IsZero() {
			if t.LastProgress.After(in.ObservedAt) || t.VerifiedEvidence != in.EvidenceID || t.ProgressHead == "" || t.ProgressPlanVersion < 1 || t.Verifier == "" || t.Milestone == "" {
				return fmt.Errorf("shadow governor: last_progress requires current verified evidence")
			}
			if (t.Head != "" && t.ProgressHead != t.Head) || (t.PlanVersion > 0 && t.ProgressPlanVersion != t.PlanVersion) {
				return fmt.Errorf("shadow governor: progress evidence does not match task generation")
			}
		}
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
		return finalizedResult(d, []string{"unknown_evidence"}, c)
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
	for _, r := range old.Recoveries {
		if r.StrategyVersion == in.StrategyVersion && r.PlanVersion == in.PlanVersion && r.Status == "accepted" && r.EffectEvidenceID == "" && (!r.EffectDueAt.After(in.ObservedAt) || r.RecurrenceID != "") {
			d.Attention = append(d.Attention, Attention{Reason: "ineffective_sol_recovery", Tier: TierAstra})
		}
	}
	sharedOverdue := 0
	for id := range actionableOverdue(in) {
		if old.OverdueTasks[id] {
			sharedOverdue++
		}
	}
	if overdue >= 2 && old.Last.Complete && sharedOverdue >= 2 {
		d.Attention = append(d.Attention, Attention{Reason: "overdue_cohort", Tier: TierAstra})
	}
	if !old.StrategyAt.IsZero() && in.ObservedAt.Before(old.StrategyAt) {
		d.Attention = append(d.Attention, Attention{Reason: "clock_rollback", Tier: TierAstra})
	}
	if !old.BaselineAt.IsZero() && len(in.Tasks) > 0 && in.ObservedAt.Sub(old.StrategyAtOrBaseline()) >= c.Watchdog {
		d.Attention = append(d.Attention, Attention{Reason: "active_watchdog", Tier: TierAstra})
	}
	return finalizedResult(d, nil, c)
}

func (s Store) RecordSolRecovery(ctx context.Context, fence int64, workspace string, r SolRecovery) error {
	if r.IncidentID == "" || r.EventID == "" || r.EvidenceID == "" || r.RequestID == "" || r.ProposedAction == "" || r.ExpectedEffect == "" || r.ActualModel != TierSol || r.Status != "accepted" || !r.Accepted || r.CompletedAt.IsZero() || r.EffectDueAt.Before(r.CompletedAt) {
		return fmt.Errorf("shadow governor: invalid Sol recovery")
	}
	return s.transform(ctx, fence, workspace, func(st *state) error {
		if st.Last.EventID != r.EventID || st.Last.EvidenceID != r.EvidenceID || st.Last.StrategyVersion != r.StrategyVersion || st.Last.PlanVersion != r.PlanVersion {
			return ErrStaleContract
		}
		if st.Recoveries == nil {
			st.Recoveries = map[string]SolRecovery{}
		}
		key := r.IncidentID + "/" + fmt.Sprint(r.StrategyVersion) + "/" + fmt.Sprint(r.PlanVersion)
		if old, ok := st.Recoveries[key]; ok {
			if old.RequestID == r.RequestID && reflect.DeepEqual(old, r) {
				return errNoMutation
			}
			return ErrStaleContract
		}
		st.Recoveries[key] = r
		return nil
	})
}
func (s Store) VerifySolRecoveryEffect(ctx context.Context, fence int64, workspace, incident, evidence string) error {
	return s.transform(ctx, fence, workspace, func(st *state) error {
		for k, r := range st.Recoveries {
			if r.IncidentID == incident {
				if evidence == "" || r.EffectEvidenceID != "" || st.Last.EvidenceID != evidence || !st.Last.Complete || st.Last.EventID == r.EventID || !st.Last.ObservedAt.After(r.CompletedAt) || st.Last.StrategyVersion != r.StrategyVersion || st.Last.PlanVersion != r.PlanVersion {
					return ErrStaleContract
				}
				r.EffectEvidenceID = evidence
				st.Recoveries[k] = r
				return nil
			}
		}
		return ErrStaleContract
	})
}

func finalizedResult(d Digest, base []string, c Config) (Result, error) {
	r := result(d, base)
	decision, reasons := r.Decision, r.Reasons
	if err := boundDigest(&r.Digest, c.MaxDigestBytes); err != nil {
		return Result{}, err
	}
	if r.Digest.Omitted > 0 {
		reasons = append(reasons, "digest_omitted")
	}
	r.Decision, r.Reasons = decision, uniqueStrings(reasons)
	return r, nil
}
func provenExternalWait(w *ExternalWait, at time.Time) bool {
	return w != nil && w.Source != "" && w.Owner != "" && w.Trigger != "" && w.Fallback != "" && w.ExpiresAt.After(at)
}
func actionableOverdue(in Observation) map[string]bool {
	out := map[string]bool{}
	for _, t := range in.Tasks {
		if !t.EvidenceDueAt.IsZero() && !t.LastProgress.After(t.EvidenceDueAt) && in.ObservedAt.After(t.EvidenceDueAt) && !provenExternalWait(t.ExternalWait, in.ObservedAt) {
			out[t.ID] = true
		}
	}
	return out
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
	sort.SliceStable(d.Attention, func(i, j int) bool {
		if tierPriority(d.Attention[i].Tier) != tierPriority(d.Attention[j].Tier) {
			return tierPriority(d.Attention[i].Tier) > tierPriority(d.Attention[j].Tier)
		}
		return d.Attention[i].TaskID+d.Attention[i].Reason < d.Attention[j].TaskID+d.Attention[j].Reason
	})
	for len(d.Attention) > 1 {
		raw, _ := json.Marshal(d)
		if len(raw) <= max {
			return nil
		}
		d.OmittedContinuation = "observation-attention"
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
func (s Store) AcknowledgeStrategy(ctx context.Context, fence int64, workspace string, receipt StrategyReceipt) error {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	return s.transform(ctx, fence, workspace, func(st *state) error {
		obs, ok := st.Observations[receipt.EventID]
		if !ok {
			return fmt.Errorf("shadow governor: unknown review event")
		}
		if receipt.EventID != st.Last.EventID || !validReceipt(receipt, obs, now) {
			return fmt.Errorf("shadow governor: ineligible review event")
		}
		if receipt.CompletedAt.Before(st.StrategyAt) {
			return ErrStaleContract
		}
		st.StrategyAt = receipt.CompletedAt
		st.BaselineAt = obs.ObservedAt
		st.ReviewBaseline = obs
		return nil
	})
}
func validReceipt(r StrategyReceipt, obs Observation, now time.Time) bool {
	if r.EventID == "" || r.RequestID == "" || r.IncidentID == "" || r.EvidenceID == "" || r.ExpectedEffect == "" || r.EffectDueAt.IsZero() || r.CompletedAt.IsZero() || r.Outcome != "completed" || !r.Accepted || r.Model != TierAstra || r.CompletedAt.After(now) {
		return false
	}
	return obs.Complete && r.CompletedAt.After(obs.ObservedAt) && !r.EffectDueAt.Before(r.CompletedAt) && r.EffectDueAt.Before(r.CompletedAt.Add(31*24*time.Hour)) && r.StrategyVersion == obs.StrategyVersion && r.PlanVersion == obs.PlanVersion && r.EvidenceID == obs.EvidenceID
}
func result(d Digest, base []string) Result {
	sort.Slice(d.Attention, func(i, j int) bool {
		return d.Attention[i].TaskID+d.Attention[i].Reason < d.Attention[j].TaskID+d.Attention[j].Reason
	})
	tier := TierNone
	reasons := append([]string(nil), base...)
	for _, a := range d.Attention {
		reasons = append(reasons, a.Reason)
		if tierPriority(a.Tier) > tierPriority(tier) {
			tier = a.Tier
		}
	}
	return Result{Decision: tier, Reasons: uniqueStrings(reasons), Digest: d, LiveBoardCollection: "unavailable: normalized snapshot supplied by caller"}
}
func tierPriority(t Tier) int {
	switch t {
	case TierAstra:
		return 4
	case TierSol:
		return 3
	case TierTerra:
		return 2
	case TierLuna:
		return 1
	default:
		return 0
	}
}
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
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
	Owner                string    `json:"owner"`
	EvidenceDueAt        time.Time `json:"evidence_due_at"`
	BlockerRoot          string    `json:"blocker_root"`
	ReassessAt           time.Time `json:"reassess_at"`
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

// ValidateProposedAction is the advisory action-boundary guard.  The Host
// remains responsible for enforcing the returned decision before any action.
func (s Store) ValidateProposedAction(ctx context.Context, workspace, task, head, generation string, strategyVersion, planVersion int, action string, now time.Time) (Contract, error) {
	c, err := s.ValidateCurrentContract(ctx, workspace, task, head, generation, strategyVersion, planVersion, now)
	if err != nil {
		return Contract{}, err
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return Contract{}, ErrStaleContract
	}
	allowed := false
	for _, a := range c.AllowedActions {
		if strings.TrimSpace(a) == action {
			allowed = true
		}
	}
	for _, a := range c.ProhibitedActions {
		if strings.TrimSpace(a) == action {
			return Contract{}, ErrStaleContract
		}
	}
	if !allowed {
		return Contract{}, ErrStaleContract
	}
	return c, nil
}
func validateContractShape(c Contract) error {
	if c.Version < 0 || c.StrategyVersion < 1 || c.PlanVersion < 1 || c.WorkspaceID == "" || c.TaskID == "" || c.Head == "" || c.Generation == "" || c.Goal == "" || c.NextAction == "" || c.ExpiresAt.IsZero() || !c.ExpiresAt.After(time.Now()) {
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
