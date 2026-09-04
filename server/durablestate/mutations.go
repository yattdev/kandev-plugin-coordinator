package durablestate

import (
	"context"
	"database/sql"
	"fmt"
)

// ErrRecordAlreadyExists is returned by AppendAdd when recordID already has
// a live current-state row.
var ErrRecordAlreadyExists = fmt.Errorf("durablestate: record already exists")

// ErrRecordNotFound is returned by AppendUpdate/appendRemove when recordID
// has no live current-state row.
var ErrRecordNotFound = fmt.Errorf("durablestate: record not found")

// ErrInvalidRecordKind is returned for a RecordKind outside §1.1's fixed
// enum.
var ErrInvalidRecordKind = fmt.Errorf("durablestate: invalid record kind")

// nextMutationID returns the next monotonic mutation_id for workspaceID
// (1 if none exist yet). Must be called from inside the write transaction
// that will insert the row using this value, so the read-then-use is
// atomic under BEGIN IMMEDIATE's write lock.
func nextMutationID(ctx context.Context, tx execer, workspaceID string) (int64, error) {
	var max sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT MAX(mutation_id) FROM mutation_log WHERE workspace_id = ?`, workspaceID,
	).Scan(&max)
	if err != nil {
		return 0, err
	}
	if !max.Valid {
		return 1, nil
	}
	return max.Int64 + 1, nil
}

// writeMutationRow appends one fully-computed Mutation to the append-only
// mutation_log. This is the single insertion point every op (add/update/
// remove, including restore_reactivation's add) funnels through, so
// "append-log-then-apply" ordering (§1.3) is enforced in exactly one place:
// callers must invoke this before mutating current_state, never after.
func writeMutationRow(ctx context.Context, tx execer, m Mutation) error {
	var beforeStorage, beforeSHA, beforeBody, beforeRef sql.NullString
	if m.Before != nil {
		beforeStorage = sql.NullString{String: string(m.Before.Storage), Valid: true}
		beforeSHA = sql.NullString{String: m.Before.SHA256, Valid: true}
		if m.Before.Storage == StorageInline {
			encoded, err := marshalBody(m.Before.Body)
			if err != nil {
				return err
			}
			beforeBody = sql.NullString{String: encoded, Valid: true}
		} else {
			beforeRef = sql.NullString{String: m.Before.Ref, Valid: true}
		}
	}
	var afterStorage, afterSHA, afterBody, afterRef sql.NullString
	if m.After != nil {
		afterStorage = sql.NullString{String: string(m.After.Storage), Valid: true}
		afterSHA = sql.NullString{String: m.After.SHA256, Valid: true}
		if m.After.Storage == StorageInline {
			encoded, err := marshalBody(m.After.Body)
			if err != nil {
				return err
			}
			afterBody = sql.NullString{String: encoded, Valid: true}
		} else {
			afterRef = sql.NullString{String: m.After.Ref, Valid: true}
		}
	}
	compactionID := sql.NullString{String: m.CompactionID, Valid: m.CompactionID != ""}
	restoreID := sql.NullString{String: m.RestoreID, Valid: m.RestoreID != ""}

	_, err := tx.ExecContext(ctx,
		`INSERT INTO mutation_log (
			workspace_id, mutation_id, timestamp, op, record_id, record_kind,
			before_storage, before_sha256, before_body, before_ref,
			after_storage, after_sha256, after_body, after_ref,
			compaction_id, restore_id, fencing_token
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.WorkspaceID, m.MutationID, m.Timestamp, string(m.Op), m.RecordID, string(m.RecordKind),
		beforeStorage, beforeSHA, beforeBody, beforeRef,
		afterStorage, afterSHA, afterBody, afterRef,
		compactionID, restoreID, m.FencingToken,
	)
	return err
}

// buildPayloadSide computes the sha256 for body and, per storage, either
// keeps it inline or content-addresses it into content_store, returning the
// resulting PayloadSide.
func buildPayloadSide(ctx context.Context, tx execer, workspaceID string, body map[string]any, storage StorageKind) (*PayloadSide, error) {
	sha, err := canonicalHash(body)
	if err != nil {
		return nil, err
	}
	switch storage {
	case StorageInline:
		return &PayloadSide{Storage: StorageInline, SHA256: sha, Body: body}, nil
	case StorageContentRef:
		ref, refSHA, err := putContentStoreRef(ctx, tx, workspaceID, body)
		if err != nil {
			return nil, err
		}
		return &PayloadSide{Storage: StorageContentRef, SHA256: refSHA, Ref: ref}, nil
	default:
		return nil, fmt.Errorf("durablestate: unknown storage kind %q", storage)
	}
}

func readCurrentStateRow(ctx context.Context, tx execer, workspaceID, recordID string) (*Record, error) {
	var kind, body, sha, updatedAt string
	err := tx.QueryRowContext(ctx,
		`SELECT record_kind, body, sha256, updated_at FROM current_state WHERE workspace_id = ? AND record_id = ?`,
		workspaceID, recordID,
	).Scan(&kind, &body, &sha, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoded, err := unmarshalBody(body)
	if err != nil {
		return nil, err
	}
	return &Record{
		WorkspaceID: workspaceID,
		RecordID:    recordID,
		RecordKind:  RecordKind(kind),
		Body:        decoded,
		SHA256:      sha,
		UpdatedAt:   updatedAt,
	}, nil
}

// AppendAdd inserts a new current-state record for recordID and appends its
// matching §1.3 "add" mutation-log entry, in one transaction, after
// checking fencingToken against workspaceID's current leader token. Fails
// with ErrRecordAlreadyExists if recordID already has a live row.
func (s *Store) AppendAdd(ctx context.Context, workspaceID string, fencingToken int64, recordID string, kind RecordKind, body map[string]any, storage StorageKind) (Mutation, error) {
	if !validRecordKinds[kind] {
		return Mutation{}, fmt.Errorf("%w: %q", ErrInvalidRecordKind, kind)
	}
	var result Mutation
	err := s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}
		existing, err := readCurrentStateRow(ctx, tx, workspaceID, recordID)
		if err != nil {
			return err
		}
		if existing != nil {
			return fmt.Errorf("%w: %q", ErrRecordAlreadyExists, recordID)
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
			Before:       nil,
			After:        after,
			FencingToken: fencingToken,
		}
		if err := writeMutationRow(ctx, tx, m); err != nil {
			return err
		}
		encoded, err := marshalBody(body)
		if err != nil {
			return err
		}
		bodySHA, err := canonicalHash(body)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO current_state (workspace_id, record_id, record_kind, body, sha256, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			workspaceID, recordID, string(kind), encoded, bodySHA, m.Timestamp,
		); err != nil {
			return err
		}
		result = m
		return nil
	})
	return result, err
}

// AppendUpdate replaces recordID's current-state body in place and appends
// its matching §1.3 "update" mutation-log entry (before = the record's
// actual prior stored body, hash-verified by construction since it is read
// from the same row being updated). Fails with ErrRecordNotFound if
// recordID has no live row.
func (s *Store) AppendUpdate(ctx context.Context, workspaceID string, fencingToken int64, recordID string, newBody map[string]any, storage StorageKind) (Mutation, error) {
	var result Mutation
	err := s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}
		existing, err := readCurrentStateRow(ctx, tx, workspaceID, recordID)
		if err != nil {
			return err
		}
		if existing == nil {
			return fmt.Errorf("%w: %q", ErrRecordNotFound, recordID)
		}
		mutationID, err := nextMutationID(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		before := &PayloadSide{Storage: StorageInline, SHA256: existing.SHA256, Body: existing.Body}
		after, err := buildPayloadSide(ctx, tx, workspaceID, newBody, storage)
		if err != nil {
			return err
		}
		m := Mutation{
			MutationID:   mutationID,
			WorkspaceID:  workspaceID,
			Timestamp:    nowUTC(),
			Op:           OpUpdate,
			RecordID:     recordID,
			RecordKind:   existing.RecordKind,
			Before:       before,
			After:        after,
			FencingToken: fencingToken,
		}
		if err := writeMutationRow(ctx, tx, m); err != nil {
			return err
		}
		encoded, err := marshalBody(newBody)
		if err != nil {
			return err
		}
		bodySHA, err := canonicalHash(newBody)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE current_state SET body = ?, sha256 = ?, updated_at = ? WHERE workspace_id = ? AND record_id = ?`,
			encoded, bodySHA, m.Timestamp, workspaceID, recordID,
		); err != nil {
			return err
		}
		result = m
		return nil
	})
	return result, err
}

// GetRecord reads recordID's live current-state row, or (nil, false) if it
// does not exist.
func (s *Store) GetRecord(ctx context.Context, workspaceID, recordID string) (*Record, bool, error) {
	rec, err := readCurrentStateRow(ctx, s.db, workspaceID, recordID)
	if err != nil {
		return nil, false, err
	}
	return rec, rec != nil, nil
}

// ListRecords returns every live current-state record for workspaceID,
// ordered by record_id, optionally filtered to a single kind (pass "" for
// every kind).
func (s *Store) ListRecords(ctx context.Context, workspaceID string, kind RecordKind) ([]Record, error) {
	var rows *sql.Rows
	var err error
	if kind == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT record_id, record_kind, body, sha256, updated_at FROM current_state WHERE workspace_id = ? ORDER BY record_id`,
			workspaceID)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT record_id, record_kind, body, sha256, updated_at FROM current_state WHERE workspace_id = ? AND record_kind = ? ORDER BY record_id`,
			workspaceID, string(kind))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var recordID, k, body, sha, updatedAt string
		if err := rows.Scan(&recordID, &k, &body, &sha, &updatedAt); err != nil {
			return nil, err
		}
		decoded, err := unmarshalBody(body)
		if err != nil {
			return nil, err
		}
		out = append(out, Record{
			WorkspaceID: workspaceID,
			RecordID:    recordID,
			RecordKind:  RecordKind(k),
			Body:        decoded,
			SHA256:      sha,
			UpdatedAt:   updatedAt,
		})
	}
	return out, rows.Err()
}
