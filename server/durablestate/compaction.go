package durablestate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// RolledRecordInput names one record the caller (the runtime/scheduler
// owner, outside this package's scope) has determined is resolved and
// ready to roll out of current state into the archive.
type RolledRecordInput struct {
	RecordID   string
	ResolvedAt string
}

// Compact performs one rollup (§2/§3/§5): it moves every record named in
// rolled out of workspaceID's materialized current state into the
// append-only archive, producing a hash-anchored compaction receipt.
//
// If compactionID is "" a new one is generated (a fresh compaction
// attempt). If compactionID names a receipt that already exists, Compact
// is idempotent per §5's crash/retry rule: a receipt stuck in the
// "archived" phase (crashed between steps (c) and (d)) has only its
// current-state swap (step d) retried, never a second archive append; a
// receipt already "committed" is returned unchanged as a no-op.
func (s *Store) Compact(ctx context.Context, workspaceID string, fencingToken int64, compactionID string, rolled []RolledRecordInput) (*CompactionReceipt, error) {
	if compactionID == "" {
		compactionID = uuid.NewString()
	}

	existing, err := s.getCompactionReceipt(ctx, workspaceID, compactionID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		switch existing.Phase {
		case "committed":
			return existing, nil
		case "archived":
			if err := s.finishCompactionSwap(ctx, workspaceID, fencingToken, existing); err != nil {
				return nil, err
			}
			return s.getCompactionReceipt(ctx, workspaceID, compactionID)
		default:
			return nil, fmt.Errorf("durablestate: compaction %q has unknown phase %q", compactionID, existing.Phase)
		}
	}

	receipt, err := s.archiveCompaction(ctx, workspaceID, fencingToken, compactionID, rolled)
	if err != nil {
		return nil, err
	}
	if err := s.finishCompactionSwap(ctx, workspaceID, fencingToken, receipt); err != nil {
		return nil, err
	}
	return s.getCompactionReceipt(ctx, workspaceID, compactionID)
}

// archiveCompaction implements §5 steps (a)-(c): compute pre/post state,
// validate §4's hash-anchored set-equality, durably append the archive and
// the corresponding remove-op mutation-log entries, and record a receipt
// in phase "archived". current_state itself is not yet touched.
func (s *Store) archiveCompaction(ctx context.Context, workspaceID string, fencingToken int64, compactionID string, rolled []RolledRecordInput) (*CompactionReceipt, error) {
	var receipt *CompactionReceipt
	err := s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}

		content, preIDs, err := loadCurrentStateContent(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		flatState, kindOf := flattenSnapshotContent(content)

		preByteCount, preSHA, err := hashSnapshotContent(content)
		if err != nil {
			return err
		}
		preWatermark, err := currentMutationWatermark(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		preSnapshotID := uuid.NewString()
		preSnap := &Snapshot{
			SnapshotID:           preSnapshotID,
			WorkspaceID:          workspaceID,
			Timestamp:            nowUTC(),
			Trigger:              TriggerPreRollup,
			Content:              content,
			ByteCount:            preByteCount,
			SHA256:               preSHA,
			RecordCount:          len(preIDs),
			RecordIDSetSHA256:    recordIDSetSHA256(preIDs),
			MutationLogWatermark: preWatermark,
			FencingToken:         fencingToken,
		}
		if err := insertSnapshot(ctx, tx, preSnap); err != nil {
			return err
		}

		var rolledIDs []string
		rolledKind := make(map[string]RecordKind)
		for _, r := range rolled {
			if _, ok := flatState[r.RecordID]; !ok {
				return fmt.Errorf("durablestate: cannot roll record %q: no such live current-state record", r.RecordID)
			}
			rolledIDs = append(rolledIDs, r.RecordID)
			rolledKind[r.RecordID] = kindOf[r.RecordID]
		}

		postIDs := make([]string, 0, len(preIDs))
		postState := make(map[string]map[string]any, len(flatState))
		rolledSet := make(map[string]bool, len(rolledIDs))
		for _, id := range rolledIDs {
			rolledSet[id] = true
		}
		for _, id := range preIDs {
			if rolledSet[id] {
				continue
			}
			postIDs = append(postIDs, id)
			postState[id] = flatState[id]
		}

		// §4 hash-anchored set-equality + disjointness, before anything is
		// mutated or archived.
		if err := VerifySetEquality(preIDs, postIDs, rolledIDs); err != nil {
			s.bumpHealthCounter(ctx, workspaceID, "compaction_verification_failures", 1)
			return err
		}

		// (c) durable archive append, sorted by record_id for a
		// deterministic archive_append hash regardless of caller-supplied
		// `rolled` ordering.
		sortedRolled := append([]string(nil), rolledIDs...)
		sort.Strings(sortedRolled)
		resolvedAtByID := make(map[string]string, len(rolled))
		for _, r := range rolled {
			resolvedAtByID[r.RecordID] = r.ResolvedAt
		}

		appendedForHash := make([]any, 0, len(sortedRolled))
		rolledRecords := make([]RolledRecord, 0, len(sortedRolled))
		appendedAt := nowUTC()

		for _, recordID := range sortedRolled {
			body := flatState[recordID]
			bodySHA, err := canonicalHash(body)
			if err != nil {
				return err
			}
			encoded, err := marshalBody(body)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO archive (workspace_id, compaction_id, record_id, record_kind, resolved_at, body, sha256, appended_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				workspaceID, compactionID, recordID, string(rolledKind[recordID]), resolvedAtByID[recordID], encoded, bodySHA, appendedAt,
			); err != nil {
				return err
			}

			mutationID, err := nextMutationID(ctx, tx, workspaceID)
			if err != nil {
				return err
			}
			ref := fmt.Sprintf("archive:%s:%s", compactionID, recordID)
			m := Mutation{
				MutationID:   mutationID,
				WorkspaceID:  workspaceID,
				Timestamp:    nowUTC(),
				Op:           OpRemove,
				RecordID:     recordID,
				RecordKind:   rolledKind[recordID],
				Before:       &PayloadSide{Storage: StorageContentRef, SHA256: bodySHA, Ref: ref},
				After:        nil,
				CompactionID: compactionID,
				FencingToken: fencingToken,
			}
			if err := writeMutationRow(ctx, tx, m); err != nil {
				return err
			}

			appendedForHash = append(appendedForHash, map[string]any{"record_id": recordID, "body": body})
			rolledRecords = append(rolledRecords, RolledRecord{
				RecordID:   recordID,
				Kind:       rolledKind[recordID],
				ResolvedAt: resolvedAtByID[recordID],
				MutationID: mutationID,
			})
		}

		appendedBytes, err := canonicalJSONBytes(appendedForHash)
		if err != nil {
			return err
		}
		// Recomputed directly from the archive rows just written (not
		// copied from the pre-computed rolled list), per §4's
		// independent-verifiability requirement.
		archiveRolledIDSetSHA := recordIDSetSHA256(sortedRolled)

		postByteCount, postSHA, err := hashSnapshotContentFlat(postState)
		if err != nil {
			return err
		}

		receipt = &CompactionReceipt{
			CompactionID: compactionID,
			WorkspaceID:  workspaceID,
			Timestamp:    nowUTC(),
			Kind:         ReceiptRollup,
			PreState: StateSummary{
				SnapshotID:        preSnapshotID,
				ByteCount:         preByteCount,
				SHA256:            preSHA,
				RecordCount:       len(preIDs),
				RecordIDSetSHA256: recordIDSetSHA256(preIDs),
			},
			RolledRecords: rolledRecords,
			PostState: StateSummary{
				SnapshotID:        "",
				ByteCount:         postByteCount,
				SHA256:            postSHA,
				RecordCount:       len(postIDs),
				RecordIDSetSHA256: recordIDSetSHA256(postIDs),
			},
			ArchiveAppend: ArchiveAppend{
				ArchivePath:             fmt.Sprintf("archive:%s", workspaceID),
				ByteCountAppended:       len(appendedBytes),
				SHA256OfAppendedBytes:   canonicalHashBytes(appendedBytes),
				RolledRecordIDSetSHA256: archiveRolledIDSetSHA,
			},
			FencingToken: fencingToken,
			Phase:        "archived",
		}
		if err := insertCompactionReceipt(ctx, tx, receipt); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("durablestate: archiving compaction %q for workspace %q: %w", compactionID, workspaceID, err)
	}
	return receipt, nil
}

// finishCompactionSwap implements §5 step (d): atomically remove every
// rolled record from current_state. Safe to call after a crash: it only
// ever deletes rows still present (a prior partial run may have already
// removed some), and never re-touches the archive/mutation log.
func (s *Store) finishCompactionSwap(ctx context.Context, workspaceID string, fencingToken int64, receipt *CompactionReceipt) error {
	return s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}
		for _, r := range receipt.RolledRecords {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM current_state WHERE workspace_id = ? AND record_id = ?`, workspaceID, r.RecordID,
			); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE compaction_receipts SET phase = 'committed' WHERE workspace_id = ? AND compaction_id = ?`,
			workspaceID, receipt.CompactionID,
		); err != nil {
			return err
		}
		return nil
	})
}

// ResumeCompactions finds every receipt for workspaceID stuck in phase
// "archived" (the process crashed between §5 steps (c) and (d)) and
// finishes step (d) for each, using the current leader's fencingToken. This
// is what a newly-elected leader (or the same leader after a restart)
// should call before doing anything else, per §5/§6.
func (s *Store) ResumeCompactions(ctx context.Context, workspaceID string, fencingToken int64) ([]*CompactionReceipt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT compaction_id FROM compaction_receipts WHERE workspace_id = ? AND phase = 'archived' ORDER BY timestamp ASC`,
		workspaceID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var resumed []*CompactionReceipt
	for _, id := range ids {
		receipt, err := s.getCompactionReceipt(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if receipt == nil || receipt.Phase != "archived" {
			continue
		}
		if err := s.finishCompactionSwap(ctx, workspaceID, fencingToken, receipt); err != nil {
			return nil, err
		}
		done, err := s.getCompactionReceipt(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		s.bumpHealthCounter(ctx, workspaceID, "compaction_recoveries", 1)
		resumed = append(resumed, done)
	}
	return resumed, nil
}

func hashSnapshotContentFlat(state map[string]map[string]any) (int, string, error) {
	encoded, err := canonicalJSONBytes(state)
	if err != nil {
		return 0, "", err
	}
	return len(encoded), canonicalHashBytes(encoded), nil
}

func insertCompactionReceipt(ctx context.Context, tx execer, r *CompactionReceipt) error {
	rolledEncoded, err := json.Marshal(r.RolledRecords)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO compaction_receipts (
			compaction_id, workspace_id, timestamp, kind, restore_id,
			pre_snapshot_id, pre_byte_count, pre_sha256, pre_record_count, pre_record_id_set_sha256,
			rolled_records,
			post_snapshot_id, post_byte_count, post_sha256, post_record_count, post_record_id_set_sha256,
			archive_path, archive_byte_count_appended, archive_sha256_of_appended_bytes, archive_rolled_record_id_set_sha256,
			fencing_token, phase
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.CompactionID, r.WorkspaceID, r.Timestamp, string(r.Kind), nullIfEmpty(r.RestoreID),
		nullIfEmpty(r.PreState.SnapshotID), r.PreState.ByteCount, r.PreState.SHA256, r.PreState.RecordCount, r.PreState.RecordIDSetSHA256,
		string(rolledEncoded),
		nullIfEmpty(r.PostState.SnapshotID), r.PostState.ByteCount, r.PostState.SHA256, r.PostState.RecordCount, r.PostState.RecordIDSetSHA256,
		r.ArchiveAppend.ArchivePath, r.ArchiveAppend.ByteCountAppended, r.ArchiveAppend.SHA256OfAppendedBytes, r.ArchiveAppend.RolledRecordIDSetSHA256,
		r.FencingToken, r.Phase,
	)
	return err
}

func nullIfEmpty(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// getCompactionReceipt reads one compaction receipt back by ID, or (nil,
// nil) if it does not exist.
func (s *Store) getCompactionReceipt(ctx context.Context, workspaceID, compactionID string) (*CompactionReceipt, error) {
	var r CompactionReceipt
	r.CompactionID = compactionID
	r.WorkspaceID = workspaceID
	var kind, restoreID, preSnapshotID, postSnapshotID sql.NullString
	var rolledEncoded string
	err := s.db.QueryRowContext(ctx,
		`SELECT timestamp, kind, restore_id,
			pre_snapshot_id, pre_byte_count, pre_sha256, pre_record_count, pre_record_id_set_sha256,
			rolled_records,
			post_snapshot_id, post_byte_count, post_sha256, post_record_count, post_record_id_set_sha256,
			archive_path, archive_byte_count_appended, archive_sha256_of_appended_bytes, archive_rolled_record_id_set_sha256,
			fencing_token, phase
		 FROM compaction_receipts WHERE workspace_id = ? AND compaction_id = ?`,
		workspaceID, compactionID,
	).Scan(
		&r.Timestamp, &kind, &restoreID,
		&preSnapshotID, &r.PreState.ByteCount, &r.PreState.SHA256, &r.PreState.RecordCount, &r.PreState.RecordIDSetSHA256,
		&rolledEncoded,
		&postSnapshotID, &r.PostState.ByteCount, &r.PostState.SHA256, &r.PostState.RecordCount, &r.PostState.RecordIDSetSHA256,
		&r.ArchiveAppend.ArchivePath, &r.ArchiveAppend.ByteCountAppended, &r.ArchiveAppend.SHA256OfAppendedBytes, &r.ArchiveAppend.RolledRecordIDSetSHA256,
		&r.FencingToken, &r.Phase,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Kind = ReceiptKind(kind.String)
	r.RestoreID = restoreID.String
	r.PreState.SnapshotID = preSnapshotID.String
	r.PostState.SnapshotID = postSnapshotID.String
	if err := json.Unmarshal([]byte(rolledEncoded), &r.RolledRecords); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListCompactionReceipts returns every compaction receipt for workspaceID
// ordered by timestamp ascending.
func (s *Store) ListCompactionReceipts(ctx context.Context, workspaceID string) ([]CompactionReceipt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT compaction_id FROM compaction_receipts WHERE workspace_id = ? ORDER BY timestamp ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]CompactionReceipt, 0, len(ids))
	for _, id := range ids {
		r, err := s.getCompactionReceipt(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}
