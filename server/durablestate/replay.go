package durablestate

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ReplayError reports corrupt or inconsistent input detected during
// mutation apply/replay (§1.3/§5/§7): a hash mismatch, an unavailable
// content_ref, a missing/mismatched compaction-receipt correlation, a
// duplicate mutation_id, or any other condition the spec requires replay
// to abort on rather than silently produce a best-effort reconstruction.
type ReplayError struct {
	msg string
}

func (e *ReplayError) Error() string { return e.msg }

func replayErrorf(format string, args ...any) error {
	return &ReplayError{msg: fmt.Sprintf(format, args...)}
}

// VerifySetEquality mirrors replay_reference.py's verify_set_equality
// (§4): pre_ids == post_ids ∪ rolled_ids AND post_ids ∩ rolled_ids == ∅.
// Returns nil on success, a *ReplayError describing exactly which
// condition failed otherwise.
func VerifySetEquality(preIDs, postIDs, rolledIDs []string) error {
	postSet := toSet(postIDs)
	rolledSet := toSet(rolledIDs)
	preSet := toSet(preIDs)

	union := make(map[string]bool, len(postSet)+len(rolledSet))
	for id := range postSet {
		union[id] = true
	}
	for id := range rolledSet {
		union[id] = true
	}
	if len(postSet)+len(rolledSet) != len(union) {
		return replayErrorf("post_ids and rolled_ids are not disjoint: a record_id appears in both (double-counted) or the inputs contain duplicates")
	}
	if len(preSet) != len(union) || !setsEqual(preSet, union) {
		missing := setDifference(union, preSet)
		extra := setDifference(preSet, union)
		return replayErrorf("pre_ids does not equal post_ids \u222a rolled_ids (unexpected in union: %v, missing from union: %v)", sortedSlice(missing), sortedSlice(extra))
	}
	return nil
}

func toSet(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func setsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func setDifference(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool)
	for k := range a {
		if !b[k] {
			out[k] = true
		}
	}
	return out
}

func sortedSlice(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// timestampAfter reports whether RFC3339Nano timestamp a names an instant
// strictly after RFC3339Nano timestamp b, comparing them as parsed time
// instants rather than as raw bytes. A byte-wise/lexicographic comparison
// of RFC3339Nano strings is unsound exactly at a whole-second boundary: a
// timestamp carrying a fractional-seconds suffix (e.g.
// "...T00:00:00.000000001Z", one nanosecond past the second) sorts
// *before* the same second with no fractional suffix (e.g.
// "...T00:00:00Z") under raw string comparison, because '.' (0x2E) is
// less than 'Z' (0x5A) -- even though the fractional-second instant is
// later in wall-clock time. That mis-ordering previously let a
// restore/replay cutoff of exactly "...T00:00:00Z" incorrectly include a
// mutation timestamped one nanosecond *after* it. Comparing parsed
// instants avoids the mis-classification; a timestamp this store did not
// itself produce in valid RFC3339Nano form is corrupt input and aborts
// replay rather than falling back to an unsound comparison.
func timestampAfter(a, b string) (bool, error) {
	ta, err := time.Parse(time.RFC3339Nano, a)
	if err != nil {
		return false, replayErrorf("cannot parse mutation timestamp %q as RFC3339Nano: %v", a, err)
	}
	tb, err := time.Parse(time.RFC3339Nano, b)
	if err != nil {
		return false, replayErrorf("cannot parse target_timestamp %q as RFC3339Nano: %v", b, err)
	}
	return ta.After(tb), nil
}

// CheckCompactionCorrelation mirrors replay_reference.py's
// check_compaction_correlation (§1.3): a compaction receipt's
// RolledRecords must be exactly the remove-op mutation-log entries
// carrying the matching CompactionID.
func CheckCompactionCorrelation(receipt *CompactionReceipt, mutationLog []Mutation) error {
	receiptByRecordID := make(map[string]RolledRecord, len(receipt.RolledRecords))
	receiptMutationIDs := make(map[int64]string, len(receipt.RolledRecords))
	for _, m := range mutationLog {
		if m.CompactionID != receipt.CompactionID {
			continue
		}
		if m.Op != OpRemove {
			return replayErrorf(
				"mutation_id %d for record_id %q carries compaction_id %q with op %q; only remove mutations may be correlated to a compaction receipt",
				m.MutationID, m.RecordID, receipt.CompactionID, m.Op,
			)
		}
	}
	for _, r := range receipt.RolledRecords {
		if existing, ok := receiptByRecordID[r.RecordID]; ok {
			return replayErrorf(
				"compaction receipt %q has duplicate rolled record_id %q (mutation_ids %d and %d); correlation is ambiguous",
				receipt.CompactionID, r.RecordID, existing.MutationID, r.MutationID,
			)
		}
		if r.MutationID == 0 {
			return replayErrorf("compaction receipt %q rolled record_id %q is missing its mutation_id", receipt.CompactionID, r.RecordID)
		}
		if existingRecordID, ok := receiptMutationIDs[r.MutationID]; ok {
			return replayErrorf(
				"compaction receipt %q has duplicate rolled mutation_id %d for record_ids %q and %q; correlation is ambiguous",
				receipt.CompactionID, r.MutationID, existingRecordID, r.RecordID,
			)
		}
		receiptByRecordID[r.RecordID] = r
		receiptMutationIDs[r.MutationID] = r.RecordID
	}

	logByRecordID := make(map[string]Mutation)
	logMutationIDs := make(map[int64]string)
	var extra, missing []string
	for _, m := range mutationLog {
		if m.CompactionID != receipt.CompactionID {
			continue
		}
		if existing, ok := logByRecordID[m.RecordID]; ok {
			return replayErrorf(
				"compaction_id %q has duplicate remove mutation entries for record_id %q (mutation_ids %d and %d); correlation is ambiguous",
				receipt.CompactionID, m.RecordID, existing.MutationID, m.MutationID,
			)
		}
		if existingRecordID, ok := logMutationIDs[m.MutationID]; ok {
			return replayErrorf(
				"compaction_id %q has duplicate remove mutation_id %d for record_ids %q and %q; replay order is ambiguous",
				receipt.CompactionID, m.MutationID, existingRecordID, m.RecordID,
			)
		}
		if _, ok := receiptByRecordID[m.RecordID]; !ok {
			extra = append(extra, m.RecordID)
			continue
		}
		logByRecordID[m.RecordID] = m
		logMutationIDs[m.MutationID] = m.RecordID
	}
	for _, r := range receipt.RolledRecords {
		m, ok := logByRecordID[r.RecordID]
		if !ok {
			missing = append(missing, r.RecordID)
			continue
		}
		if m.MutationID != r.MutationID {
			return replayErrorf(
				"compaction receipt %q rolled record_id %q declares mutation_id %d, but the correlated remove mutation has mutation_id %d",
				receipt.CompactionID, r.RecordID, r.MutationID, m.MutationID,
			)
		}
	}
	if len(extra) > 0 || len(missing) > 0 {
		sort.Strings(extra)
		sort.Strings(missing)
		return replayErrorf(
			"compaction receipt rolled_records does not exactly match mutation-log remove entries for compaction_id %q: missing=%v extra=%v",
			receipt.CompactionID, missing, extra,
		)
	}
	return nil
}

// applyMutationInMemory mirrors replay_reference.py's apply_mutation:
// applies one mutation-log entry to a working state map (record_id ->
// body), verifying before/after hashes, and returns the new state (the
// input is not mutated, mirroring §5's "prepare in full, validate, swap"
// discipline). payloadStore resolves content_ref sides.
func applyMutationInMemory(ctx context.Context, tx execer, workspaceID string, state map[string]map[string]any, m Mutation) (map[string]map[string]any, error) {
	newState := make(map[string]map[string]any, len(state)+1)
	for k, v := range state {
		newState[k] = v
	}

	switch m.Op {
	case OpAdd:
		if _, exists := state[m.RecordID]; exists {
			return nil, replayErrorf("add for already-present record_id %q", m.RecordID)
		}
		if m.CompactionID != "" {
			return nil, replayErrorf("add mutation for %q must carry compaction_id: null (add is never rollup-driven)", m.RecordID)
		}
		after, err := resolvePayload(ctx, tx, workspaceID, m.After)
		if err != nil {
			return nil, err
		}
		if after == nil {
			return nil, replayErrorf("add mutation for %q is missing 'after'", m.RecordID)
		}
		newState[m.RecordID] = after

	case OpUpdate:
		existing, ok := state[m.RecordID]
		if !ok {
			return nil, replayErrorf("update for missing record_id %q", m.RecordID)
		}
		if m.CompactionID != "" {
			return nil, replayErrorf("update mutation for %q must carry compaction_id: null (update is never rollup-driven)", m.RecordID)
		}
		before, err := resolvePayload(ctx, tx, workspaceID, m.Before)
		if err != nil {
			return nil, err
		}
		existingHash, err := canonicalHash(existing)
		if err != nil {
			return nil, err
		}
		beforeHash, err := canonicalHash(before)
		if before == nil || err != nil || existingHash != beforeHash {
			return nil, replayErrorf("update before-state mismatch for %q: working state does not match the mutation's declared before body (possible duplicate replay or corrupt input)", m.RecordID)
		}
		after, err := resolvePayload(ctx, tx, workspaceID, m.After)
		if err != nil {
			return nil, err
		}
		if after == nil {
			return nil, replayErrorf("update mutation for %q is missing 'after'", m.RecordID)
		}
		newState[m.RecordID] = after

	case OpRemove:
		existing, ok := state[m.RecordID]
		if !ok {
			return nil, replayErrorf("remove for missing record_id %q", m.RecordID)
		}
		if m.CompactionID == "" {
			return nil, replayErrorf("remove mutation for %q is missing the required compaction_id correlation (§1.3: every remove entry MUST carry the compaction_id of the rollup that produced it)", m.RecordID)
		}
		before, err := resolvePayload(ctx, tx, workspaceID, m.Before)
		if err != nil {
			return nil, err
		}
		existingHash, err := canonicalHash(existing)
		if err != nil {
			return nil, err
		}
		beforeHash, err := canonicalHash(before)
		if before == nil || err != nil || existingHash != beforeHash {
			return nil, replayErrorf("remove before-state mismatch for %q: working state does not match the mutation's declared before body", m.RecordID)
		}
		delete(newState, m.RecordID)

	default:
		return nil, replayErrorf("unknown op %q", m.Op)
	}
	return newState, nil
}

// ReplayOptions configures Replay's target cutoff and correlation set.
type ReplayOptions struct {
	// TargetMutationID, if non-nil, stops replay after applying the last
	// mutation with mutation_id <= *TargetMutationID.
	TargetMutationID *int64
	// TargetTimestamp, if non-empty, stops replay after applying the last
	// mutation whose timestamp, parsed as an RFC3339Nano instant, is at or
	// before TargetTimestamp (also parsed as an RFC3339Nano instant --
	// never a raw byte/lexicographic comparison of the two strings; see
	// timestampAfter).
	TargetTimestamp string
}

// Replay deterministically reconstructs current state (§7 step 3) starting
// from snapshotID's content, applying every mutation-log entry for
// workspaceID with mutation_id greater than the snapshot's
// mutation_log_watermark, in ascending mutation_id order, up to opts'
// cutoff. Every remove mutation replayed is cross-checked against a known
// compaction receipt (§1.3/§7 step 3); a remove correlating to no known
// receipt, a hash mismatch, an unavailable content_ref, or a duplicate
// mutation_id all abort replay as corrupt input (fail closed, §7 step 4),
// never a best-effort partial result.
func (s *Store) Replay(ctx context.Context, workspaceID, snapshotID string, opts ReplayOptions) (map[string]map[string]any, error) {
	snap, err := s.GetSnapshot(ctx, workspaceID, snapshotID)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, fmt.Errorf("durablestate: no such snapshot %q for workspace %q", snapshotID, workspaceID)
	}
	if err := validateSnapshotAnchors(snap); err != nil {
		s.bumpHealthCounter(ctx, workspaceID, "replay_failures", 1)
		return nil, err
	}
	state, _ := flattenSnapshotContent(snap.Content)

	var watermark int64
	if snap.MutationLogWatermark != nil {
		watermark = *snap.MutationLogWatermark
	}

	mutations, err := s.mutationsAfter(ctx, workspaceID, watermark)
	if err != nil {
		return nil, err
	}
	result, err := s.replayMutations(ctx, workspaceID, state, mutations, opts)
	if err != nil {
		if _, ok := err.(*ReplayError); ok {
			s.bumpHealthCounter(ctx, workspaceID, "replay_failures", 1)
		}
		return nil, err
	}
	return result, nil
}

// replayMutations is Replay's pure core, mirroring replay_reference.py's
// replay(): sorts by mutation_id (the only valid ordering input), rejects
// duplicate mutation_ids, determines the subsequence actually applied
// under opts' cutoff, cross-checks every remove's compaction_id against a
// known receipt *before* applying anything, then applies in order.
func (s *Store) replayMutations(ctx context.Context, workspaceID string, state map[string]map[string]any, mutationLog []Mutation, opts ReplayOptions) (map[string]map[string]any, error) {
	seen := make(map[int64]bool, len(mutationLog))
	for _, m := range mutationLog {
		if seen[m.MutationID] {
			return nil, replayErrorf("duplicate mutation_id %d in mutation log; replay order is ambiguous and cannot be deterministic", m.MutationID)
		}
		seen[m.MutationID] = true
	}
	ordered := append([]Mutation(nil), mutationLog...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].MutationID < ordered[j].MutationID })

	var applied []Mutation
	for _, m := range ordered {
		if opts.TargetMutationID != nil && m.MutationID > *opts.TargetMutationID {
			break
		}
		if opts.TargetTimestamp != "" {
			after, err := timestampAfter(m.Timestamp, opts.TargetTimestamp)
			if err != nil {
				return nil, err
			}
			if after {
				break
			}
		}
		applied = append(applied, m)
	}

	removeCompactionIDs := make(map[string]bool)
	for _, m := range applied {
		if m.Op == OpRemove && m.CompactionID != "" {
			removeCompactionIDs[m.CompactionID] = true
		}
	}
	for compactionID := range removeCompactionIDs {
		receipt, err := s.getCompactionReceipt(ctx, workspaceID, compactionID)
		if err != nil {
			return nil, err
		}
		if receipt == nil {
			return nil, replayErrorf(
				"a remove mutation being replayed references compaction_id %q, but no matching receipt was found (absent receipt set, or an unknown/nonexistent compaction_id); a remove cannot be replayed without correlating it against its rollup receipt",
				compactionID,
			)
		}
		if err := CheckCompactionCorrelation(receipt, mutationLog); err != nil {
			return nil, err
		}
	}

	current := make(map[string]map[string]any, len(state))
	for k, v := range state {
		current[k] = v
	}
	for _, m := range applied {
		next, err := applyMutationInMemory(ctx, s.db, workspaceID, current, m)
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

// mutationsAfter returns every mutation-log entry for workspaceID with
// mutation_id strictly greater than watermark, in no particular order
// (replayMutations sorts before applying).
func (s *Store) mutationsAfter(ctx context.Context, workspaceID string, watermark int64) ([]Mutation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mutation_id, timestamp, op, record_id, record_kind,
			before_storage, before_sha256, before_body, before_ref,
			after_storage, after_sha256, after_body, after_ref,
			compaction_id, restore_id, fencing_token
		 FROM mutation_log WHERE workspace_id = ? AND mutation_id > ? ORDER BY mutation_id`,
		workspaceID, watermark)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mutation
	for rows.Next() {
		m, err := scanMutationRow(workspaceID, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
