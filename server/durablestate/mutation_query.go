package durablestate

import (
	"context"
	"database/sql"
)

// scanMutationRow scans one mutation_log row (in the exact column order
// mutationsAfter/ListMutations select) into a Mutation.
func scanMutationRow(workspaceID string, rows *sql.Rows) (Mutation, error) {
	var m Mutation
	m.WorkspaceID = workspaceID
	var op, kind string
	var beforeStorage, beforeSHA, beforeBody, beforeRef sql.NullString
	var afterStorage, afterSHA, afterBody, afterRef sql.NullString
	var compactionID, restoreID sql.NullString
	if err := rows.Scan(
		&m.MutationID, &m.Timestamp, &op, &m.RecordID, &kind,
		&beforeStorage, &beforeSHA, &beforeBody, &beforeRef,
		&afterStorage, &afterSHA, &afterBody, &afterRef,
		&compactionID, &restoreID, &m.FencingToken,
	); err != nil {
		return Mutation{}, err
	}
	m.Op = MutationOp(op)
	m.RecordKind = RecordKind(kind)
	m.CompactionID = compactionID.String
	m.RestoreID = restoreID.String

	if beforeStorage.Valid {
		side := &PayloadSide{Storage: StorageKind(beforeStorage.String), SHA256: beforeSHA.String}
		if side.Storage == StorageInline && beforeBody.Valid {
			body, err := unmarshalBody(beforeBody.String)
			if err != nil {
				return Mutation{}, err
			}
			side.Body = body
		} else {
			side.Ref = beforeRef.String
		}
		m.Before = side
	}
	if afterStorage.Valid {
		side := &PayloadSide{Storage: StorageKind(afterStorage.String), SHA256: afterSHA.String}
		if side.Storage == StorageInline && afterBody.Valid {
			body, err := unmarshalBody(afterBody.String)
			if err != nil {
				return Mutation{}, err
			}
			side.Body = body
		} else {
			side.Ref = afterRef.String
		}
		m.After = side
	}
	return m, nil
}

// ListMutations returns every mutation-log entry for workspaceID in
// ascending mutation_id order.
func (s *Store) ListMutations(ctx context.Context, workspaceID string) ([]Mutation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mutation_id, timestamp, op, record_id, record_kind,
			before_storage, before_sha256, before_body, before_ref,
			after_storage, after_sha256, after_body, after_ref,
			compaction_id, restore_id, fencing_token
		 FROM mutation_log WHERE workspace_id = ? ORDER BY mutation_id`,
		workspaceID)
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

// getMutationsByCompactionID returns every mutation-log entry tagged with
// compactionID, in no particular order. Used to revalidate a compaction
// receipt's rolled_records against what the mutation log still durably
// shows for that compaction_id (§1.3/§4), rather than trusting the
// receipt's own recorded correlation.
func getMutationsByCompactionID(ctx context.Context, tx execer, workspaceID, compactionID string) ([]Mutation, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT mutation_id, timestamp, op, record_id, record_kind,
			before_storage, before_sha256, before_body, before_ref,
			after_storage, after_sha256, after_body, after_ref,
			compaction_id, restore_id, fencing_token
		 FROM mutation_log WHERE workspace_id = ? AND compaction_id = ?`,
		workspaceID, compactionID)
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

// getMutationByRestoreID returns the single append-only mutation-log entry
// tagged with restoreID (the add-op appendReactivationMutation wrote), or
// nil if none exists. Used by finishReactivationApply so a retry of
// ReactivateRecord can never apply a body other than the one already
// durably recorded for this restore_id — the mutation log, not any
// caller-supplied argument, is the sole source of truth for what a retry
// commits.
func getMutationByRestoreID(ctx context.Context, tx execer, workspaceID, restoreID string) (*Mutation, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT mutation_id, timestamp, op, record_id, record_kind,
			before_storage, before_sha256, before_body, before_ref,
			after_storage, after_sha256, after_body, after_ref,
			compaction_id, restore_id, fencing_token
		 FROM mutation_log WHERE workspace_id = ? AND restore_id = ?`,
		workspaceID, restoreID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	m, err := scanMutationRow(workspaceID, rows)
	if err != nil {
		return nil, err
	}
	return &m, nil
}
