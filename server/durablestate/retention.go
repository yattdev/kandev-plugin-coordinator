package durablestate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// PrunableSnapshotIDs mirrors replay_reference.py's prunable_snapshot_ids
// (§1.2): the newest keepCount full snapshots (across all trigger types,
// ordered by timestamp) are retained unconditionally; this returns the
// snapshot_ids outside that window that MAY be considered for pruning. The
// caller (PruneSnapshots, or an external caller with its own bridging
// check) must still separately confirm the mutation-log-bridging condition
// before actually pruning any given one — this only applies the
// count-based floor.
func (s *Store) PrunableSnapshotIDs(ctx context.Context, workspaceID string, keepCount int) ([]string, error) {
	snaps, err := s.ListSnapshots(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Timestamp > snaps[j].Timestamp })
	if keepCount >= len(snaps) {
		return nil, nil
	}
	out := make([]string, 0, len(snaps)-keepCount)
	for _, s := range snaps[keepCount:] {
		out = append(out, s.SnapshotID)
	}
	return out, nil
}

// PrunableMutationIDs mirrors replay_reference.py's prunable_mutation_ids
// (§1.3): mutation-log entries older than the oldest *retained* snapshot's
// mutation_log_watermark may be pruned, since no retained snapshot needs
// them for replay.
func (s *Store) PrunableMutationIDs(ctx context.Context, workspaceID string, retainedSnapshotIDs []string) ([]int64, error) {
	return prunableMutationIDs(ctx, s.db, workspaceID, retainedSnapshotIDs)
}

func prunableMutationIDs(ctx context.Context, tx execer, workspaceID string, retainedSnapshotIDs []string) ([]int64, error) {
	var watermark *int64
	for _, id := range retainedSnapshotIDs {
		snap, err := scanSnapshot(ctx, tx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if snap == nil || snap.MutationLogWatermark == nil {
			continue
		}
		if watermark == nil || *snap.MutationLogWatermark < *watermark {
			v := *snap.MutationLogWatermark
			watermark = &v
		}
	}
	if watermark == nil {
		return nil, nil
	}
	mutations, err := listMutations(ctx, tx, workspaceID)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, m := range mutations {
		if m.MutationID <= *watermark {
			out = append(out, m.MutationID)
		}
	}
	return out, nil
}

// PruneSnapshots removes every snapshot named by PrunableSnapshotIDs(ctx,
// workspaceID, keepCount) and durably receipts the pruning itself as a
// compaction receipt with kind "snapshot_prune" and rolled_records naming
// the pruned snapshot_ids (never actual state records), per §1.2. Checked
// against fencingToken like any other mutation of the archive's retained-
// snapshot index.
func (s *Store) PruneSnapshots(ctx context.Context, workspaceID string, fencingToken int64, keepCount int) (*CompactionReceipt, error) {
	prunable, err := s.PrunableSnapshotIDs(ctx, workspaceID, keepCount)
	if err != nil {
		return nil, err
	}
	if len(prunable) == 0 {
		return nil, nil
	}
	var receipt *CompactionReceipt
	err = s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}
		for _, id := range prunable {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM snapshots WHERE workspace_id = ? AND snapshot_id = ?`, workspaceID, id,
			); err != nil {
				return err
			}
		}
		rolled := make([]RolledRecord, 0, len(prunable))
		for _, id := range prunable {
			rolled = append(rolled, RolledRecord{RecordID: id})
		}
		receipt = &CompactionReceipt{
			CompactionID:  uuid.NewString(),
			WorkspaceID:   workspaceID,
			Timestamp:     nowUTC(),
			Kind:          ReceiptSnapshotPrune,
			RolledRecords: rolled,
			ArchiveAppend: ArchiveAppend{RolledRecordIDSetSHA256: recordIDSetSHA256(prunable)},
			FencingToken:  fencingToken,
			Phase:         "committed",
		}
		return insertCompactionReceipt(ctx, tx, receipt)
	})
	if err != nil {
		return nil, fmt.Errorf("durablestate: pruning snapshots for workspace %q: %w", workspaceID, err)
	}
	return receipt, nil
}

// PruneMutations removes every mutation-log entry named by
// PrunableMutationIDs(ctx, workspaceID, retainedSnapshotIDs), except any
// mutation still protected because it is named by rolled_records[].
// mutation_id on a compaction/restore_reactivation receipt that has not
// yet reached phase "committed" (still "archived"). Those specific rows
// are exactly the durable evidence verifyRolledRecordMutationLinkage
// (compaction.go) needs to independently re-derive trust in that receipt
// before ResumeCompactions can finish its current-state swap; pruning one
// out from under a crashed, not-yet-committed rollup would permanently
// strand it (fail closed forever, since the very evidence needed to ever
// safely commit it would be gone), which is the concrete case
// STATE_COMPACTION_SPEC.md's snapshot/retention cadence requirement --
// "required bodies/references remain available" -- exists to prevent.
// Like every other write through this package's single fencing boundary
// (§6), this is checked against fencingToken so a stale, fenced-out leader
// cannot prune append-only history out from under the current one.
func (s *Store) PruneMutations(ctx context.Context, workspaceID string, fencingToken int64, retainedSnapshotIDs []string) ([]int64, error) {
	var pruned []int64
	err := s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}
		prunable, err := prunableMutationIDs(ctx, tx, workspaceID, retainedSnapshotIDs)
		if err != nil {
			return err
		}
		if len(prunable) == 0 {
			return nil
		}
		protected, err := protectedMutationIDs(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		for _, id := range prunable {
			if protected[id] {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM mutation_log WHERE workspace_id = ? AND mutation_id = ?`, workspaceID, id,
			); err != nil {
				return err
			}
			pruned = append(pruned, id)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("durablestate: pruning mutations for workspace %q: %w", workspaceID, err)
	}
	return pruned, nil
}

// protectedMutationIDs returns the set of mutation_ids named by
// rolled_records[].mutation_id on any compaction/restore_reactivation
// receipt in workspaceID that has not yet reached phase "committed" (i.e.
// is still "archived"). See PruneMutations for why these must survive
// pruning regardless of snapshot watermark.
func (s *Store) protectedMutationIDs(ctx context.Context, workspaceID string) (map[int64]bool, error) {
	return protectedMutationIDs(ctx, s.db, workspaceID)
}

func protectedMutationIDs(ctx context.Context, tx execer, workspaceID string) (map[int64]bool, error) {
	receipts, err := listCompactionReceipts(ctx, tx, workspaceID)
	if err != nil {
		return nil, err
	}
	protected := make(map[int64]bool)
	for _, r := range receipts {
		if r.Phase == "committed" {
			continue
		}
		for _, rec := range r.RolledRecords {
			if rec.MutationID != 0 {
				protected[rec.MutationID] = true
			}
		}
	}
	return protected, nil
}

// SaveReplayCheckpoint records "successfully applied up to mutation_id = k"
// for a named replay run (§5's replay crash/retry guard), so a crash
// mid-replay resumes from k+1 rather than restarting from the base
// snapshot.
func (s *Store) SaveReplayCheckpoint(ctx context.Context, workspaceID, checkpointKey string, lastAppliedMutationID int64) error {
	return s.withWriteTx(ctx, func(tx execer) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO replay_checkpoints (workspace_id, checkpoint_key, last_applied_mutation, updated_at)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT (workspace_id, checkpoint_key) DO UPDATE SET last_applied_mutation = excluded.last_applied_mutation, updated_at = excluded.updated_at`,
			workspaceID, checkpointKey, lastAppliedMutationID, nowUTC())
		return err
	})
}

// LoadReplayCheckpoint returns the last checkpointed mutation_id for a
// named replay run, or (0, false) if none has been saved yet.
func (s *Store) LoadReplayCheckpoint(ctx context.Context, workspaceID, checkpointKey string) (int64, bool, error) {
	var v int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_applied_mutation FROM replay_checkpoints WHERE workspace_id = ? AND checkpoint_key = ?`,
		workspaceID, checkpointKey,
	).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return v, true, nil
}

// ReplayFromCheckpoint resumes a checkpointed replay run: if a checkpoint
// exists for checkpointKey, replay only applies mutations after it,
// starting the working state from baseState (the caller's last known
// materialized state as of that checkpoint) rather than re-reading the
// base snapshot; if no checkpoint exists yet, it behaves like Replay
// starting fresh from snapshotID. After each mutation is successfully
// applied, callers doing very large replay ranges should call
// SaveReplayCheckpoint themselves at whatever granularity suits them —
// this method applies the whole requested range in one call and saves one
// checkpoint at the end, since SQLite transactions here are already
// all-or-nothing per call.
func (s *Store) ReplayFromCheckpoint(ctx context.Context, workspaceID, snapshotID, checkpointKey string, opts ReplayOptions) (map[string]map[string]any, error) {
	lastApplied, ok, err := s.LoadReplayCheckpoint(ctx, workspaceID, checkpointKey)
	if err != nil {
		return nil, err
	}
	var state map[string]map[string]any
	var startAfter int64
	if ok {
		// Resume: rebuild state from the snapshot up through the
		// checkpoint (mutation_id <= k), a cheap re-derivation since §1.3's
		// hash checks make it safe to recompute rather than persist the
		// full intermediate state; then continue applying from k+1.
		checkpointOpts := ReplayOptions{TargetMutationID: &lastApplied}
		state, err = s.Replay(ctx, workspaceID, snapshotID, checkpointOpts)
		if err != nil {
			return nil, err
		}
		startAfter = lastApplied
	} else {
		snap, err := s.GetSnapshot(ctx, workspaceID, snapshotID)
		if err != nil {
			return nil, err
		}
		if snap == nil {
			return nil, fmt.Errorf("durablestate: no such snapshot %q for workspace %q", snapshotID, workspaceID)
		}
		state, _ = flattenSnapshotContent(snap.Content)
		if snap.MutationLogWatermark != nil {
			startAfter = *snap.MutationLogWatermark
		}
	}

	mutations, err := s.mutationsAfter(ctx, workspaceID, startAfter)
	if err != nil {
		return nil, err
	}
	final, err := s.replayMutations(ctx, workspaceID, state, mutations, opts)
	if err != nil {
		return nil, err
	}
	var maxApplied int64 = startAfter
	for _, m := range mutations {
		if opts.TargetMutationID != nil && m.MutationID > *opts.TargetMutationID {
			continue
		}
		if opts.TargetTimestamp != "" {
			after, err := timestampAfter(m.Timestamp, opts.TargetTimestamp)
			if err != nil {
				return nil, err
			}
			if after {
				continue
			}
		}
		if m.MutationID > maxApplied {
			maxApplied = m.MutationID
		}
	}
	if err := s.SaveReplayCheckpoint(ctx, workspaceID, checkpointKey, maxApplied); err != nil {
		return nil, err
	}
	return final, nil
}
