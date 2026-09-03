package durablestate

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ReactivateRecord implements §7 step 5's restore_reactivation: it
// re-activates a reconstructed historical record as a live current-state
// obligation again. Unlike the read-only inspection in steps 1-4, this is
// a mutation of current state and therefore goes through the same fenced
// write boundary as any other mutation: it appends its own add-op
// mutation-log entry (compaction_id: null, restore_id: restoreID) and
// carries a receipt in §3's shape (kind: restore_reactivation,
// RolledRecords repurposed as the reactivated-records list, per the
// spec's note that this receipt is "the same shape... with rolled_records
// replaced by a reactivated_records list of the same shape").
//
// Idempotent per §5: if restoreID names a receipt already in phase
// "committed", this is a no-op returning the existing receipt. If it names
// one stuck in phase "archived" (crashed after the mutation-log append but
// before the current-state write), only the current-state write is
// retried — the mutation-log entry is never re-appended.
func (s *Store) ReactivateRecord(ctx context.Context, workspaceID string, fencingToken int64, restoreID, recordID string, kind RecordKind, body map[string]any, storage StorageKind) (*CompactionReceipt, error) {
	if restoreID == "" {
		restoreID = uuid.NewString()
	}
	if !validRecordKinds[kind] {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRecordKind, kind)
	}

	existing, err := s.getRestoreReceipt(ctx, workspaceID, restoreID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		switch existing.Phase {
		case "committed":
			return existing, nil
		case "archived":
			if err := s.finishReactivationApply(ctx, workspaceID, fencingToken, existing, body); err != nil {
				return nil, err
			}
			return s.getRestoreReceipt(ctx, workspaceID, restoreID)
		default:
			return nil, fmt.Errorf("durablestate: restore_reactivation %q has unknown phase %q", restoreID, existing.Phase)
		}
	}

	receipt, err := s.appendReactivationMutation(ctx, workspaceID, fencingToken, restoreID, recordID, kind, body, storage)
	if err != nil {
		return nil, err
	}
	if err := s.finishReactivationApply(ctx, workspaceID, fencingToken, receipt, body); err != nil {
		return nil, err
	}
	return s.getRestoreReceipt(ctx, workspaceID, restoreID)
}

func (s *Store) appendReactivationMutation(ctx context.Context, workspaceID string, fencingToken int64, restoreID, recordID string, kind RecordKind, body map[string]any, storage StorageKind) (*CompactionReceipt, error) {
	var receipt *CompactionReceipt
	err := s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}
		existing, err := readCurrentStateRow(ctx, tx, workspaceID, recordID)
		if err != nil {
			return err
		}
		if existing != nil {
			return fmt.Errorf("%w: %q (cannot reactivate a record that is already live)", ErrRecordAlreadyExists, recordID)
		}
		mutationID, err := nextMutationID(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		after, err := buildPayloadSide(ctx, tx, workspaceID, body, storage)
		if err != nil {
			return err
		}
		m := Mutation{
			MutationID:   mutationID,
			WorkspaceID:  workspaceID,
			Timestamp:    nowUTC(),
			Op:           OpAdd,
			RecordID:     recordID,
			RecordKind:   kind,
			After:        after,
			RestoreID:    restoreID,
			FencingToken: fencingToken,
		}
		if err := writeMutationRow(ctx, tx, m); err != nil {
			return err
		}
		receipt = &CompactionReceipt{
			CompactionID: restoreID,
			WorkspaceID:  workspaceID,
			Timestamp:    nowUTC(),
			Kind:         ReceiptRestoreReactivation,
			RestoreID:    restoreID,
			RolledRecords: []RolledRecord{
				{RecordID: recordID, Kind: kind, MutationID: mutationID},
			},
			FencingToken: fencingToken,
			Phase:        "archived",
		}
		return insertCompactionReceipt(ctx, tx, receipt)
	})
	if err != nil {
		return nil, fmt.Errorf("durablestate: appending restore_reactivation %q for workspace %q: %w", restoreID, workspaceID, err)
	}
	return receipt, nil
}

func (s *Store) finishReactivationApply(ctx context.Context, workspaceID string, fencingToken int64, receipt *CompactionReceipt, body map[string]any) error {
	return s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}
		if len(receipt.RolledRecords) != 1 {
			return fmt.Errorf("durablestate: restore_reactivation receipt %q has unexpected reactivated-record count %d", receipt.CompactionID, len(receipt.RolledRecords))
		}
		reactivated := receipt.RolledRecords[0]
		existing, err := readCurrentStateRow(ctx, tx, workspaceID, reactivated.RecordID)
		if err != nil {
			return err
		}
		if existing == nil {
			encoded, err := marshalBody(body)
			if err != nil {
				return err
			}
			sha, err := canonicalHash(body)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO current_state (workspace_id, record_id, record_kind, body, sha256, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
				workspaceID, reactivated.RecordID, string(reactivated.Kind), encoded, sha, nowUTC(),
			); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE compaction_receipts SET phase = 'committed' WHERE workspace_id = ? AND compaction_id = ?`,
			workspaceID, receipt.CompactionID)
		return err
	})
}

func (s *Store) getRestoreReceipt(ctx context.Context, workspaceID, restoreID string) (*CompactionReceipt, error) {
	return s.getCompactionReceipt(ctx, workspaceID, restoreID)
}
