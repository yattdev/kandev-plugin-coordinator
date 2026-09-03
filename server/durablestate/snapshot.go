package durablestate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// CaptureSnapshot builds and durably stores a full, self-contained
// current-state snapshot (§1.1) for workspaceID: every live record, in
// full, grouped by kind, plus its byte_count/sha256/record_count/
// record_id_set_sha256 and the mutation_log_watermark (the last
// mutation_id already reflected in this content). fencingToken is checked
// like any other mutating call — a snapshot capture is itself durably
// recorded and therefore goes through the same fencing guard, even though
// it does not touch current_state or the mutation log.
func (s *Store) CaptureSnapshot(ctx context.Context, workspaceID string, trigger SnapshotTrigger, fencingToken int64) (*Snapshot, error) {
	var snap *Snapshot
	err := s.withWriteTx(ctx, func(tx execer) error {
		if err := checkFencing(ctx, tx, workspaceID, fencingToken); err != nil {
			return err
		}
		content, recordIDs, err := loadCurrentStateContent(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		watermark, err := currentMutationWatermark(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		byteCount, sha, err := hashSnapshotContent(content)
		if err != nil {
			return err
		}
		s := &Snapshot{
			SnapshotID:           uuid.NewString(),
			WorkspaceID:          workspaceID,
			Timestamp:            nowUTC(),
			Trigger:              trigger,
			Content:              content,
			ByteCount:            byteCount,
			SHA256:               sha,
			RecordCount:          len(recordIDs),
			RecordIDSetSHA256:    recordIDSetSHA256(recordIDs),
			MutationLogWatermark: watermark,
			FencingToken:         fencingToken,
		}
		if err := insertSnapshot(ctx, tx, s); err != nil {
			return err
		}
		snap = s
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("durablestate: capturing snapshot for workspace %q: %w", workspaceID, err)
	}
	return snap, nil
}

// loadCurrentStateContent reads every live current-state row for
// workspaceID into §1.1's content shape (record_kind -> record_id ->
// body), plus the flat list of every record_id present (for
// record_id_set_sha256).
func loadCurrentStateContent(ctx context.Context, tx execer, workspaceID string) (map[string]map[string]map[string]any, []string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT record_id, record_kind, body FROM current_state WHERE workspace_id = ? ORDER BY record_id`, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	content := make(map[string]map[string]map[string]any)
	var ids []string
	for rows.Next() {
		var recordID, kind, body string
		if err := rows.Scan(&recordID, &kind, &body); err != nil {
			return nil, nil, err
		}
		decoded, err := unmarshalBody(body)
		if err != nil {
			return nil, nil, err
		}
		if content[kind] == nil {
			content[kind] = make(map[string]map[string]any)
		}
		content[kind][recordID] = decoded
		ids = append(ids, recordID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return content, ids, nil
}

// currentMutationWatermark returns the highest mutation_id recorded for
// workspaceID, or nil if none exist yet.
func currentMutationWatermark(ctx context.Context, tx execer, workspaceID string) (*int64, error) {
	var max sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(mutation_id) FROM mutation_log WHERE workspace_id = ?`, workspaceID,
	).Scan(&max); err != nil {
		return nil, err
	}
	if !max.Valid {
		return nil, nil
	}
	v := max.Int64
	return &v, nil
}

// hashSnapshotContent computes §1.1's byte_count/sha256 over the canonical
// serialization of a snapshot's content map.
func hashSnapshotContent(content map[string]map[string]map[string]any) (int, string, error) {
	encoded, err := canonicalJSONBytes(content)
	if err != nil {
		return 0, "", err
	}
	return len(encoded), canonicalHashBytes(encoded), nil
}

func insertSnapshot(ctx context.Context, tx execer, snap *Snapshot) error {
	contentEncoded, err := json.Marshal(snap.Content)
	if err != nil {
		return err
	}
	var watermark sql.NullInt64
	if snap.MutationLogWatermark != nil {
		watermark = sql.NullInt64{Int64: *snap.MutationLogWatermark, Valid: true}
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO snapshots (
			snapshot_id, workspace_id, timestamp, trigger, content, byte_count, sha256,
			record_count, record_id_set_sha256, mutation_log_watermark, fencing_token
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snap.SnapshotID, snap.WorkspaceID, snap.Timestamp, string(snap.Trigger), string(contentEncoded),
		snap.ByteCount, snap.SHA256, snap.RecordCount, snap.RecordIDSetSHA256, watermark, snap.FencingToken,
	)
	return err
}

// GetSnapshot reads one snapshot back by ID.
func (s *Store) GetSnapshot(ctx context.Context, workspaceID, snapshotID string) (*Snapshot, error) {
	return scanSnapshot(ctx, s.db, workspaceID, snapshotID)
}

func scanSnapshot(ctx context.Context, tx execer, workspaceID, snapshotID string) (*Snapshot, error) {
	var trigger, contentEncoded, sha, recordIDSetSHA string
	var timestamp string
	var byteCount, recordCount int
	var watermark sql.NullInt64
	var fencingToken int64
	err := tx.QueryRowContext(ctx,
		`SELECT timestamp, trigger, content, byte_count, sha256, record_count, record_id_set_sha256, mutation_log_watermark, fencing_token
		 FROM snapshots WHERE workspace_id = ? AND snapshot_id = ?`, workspaceID, snapshotID,
	).Scan(&timestamp, &trigger, &contentEncoded, &byteCount, &sha, &recordCount, &recordIDSetSHA, &watermark, &fencingToken)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var content map[string]map[string]map[string]any
	if err := json.Unmarshal([]byte(contentEncoded), &content); err != nil {
		return nil, err
	}
	var wPtr *int64
	if watermark.Valid {
		wPtr = &watermark.Int64
	}
	return &Snapshot{
		SnapshotID:           snapshotID,
		WorkspaceID:          workspaceID,
		Timestamp:            timestamp,
		Trigger:              SnapshotTrigger(trigger),
		Content:              content,
		ByteCount:            byteCount,
		SHA256:               sha,
		RecordCount:          recordCount,
		RecordIDSetSHA256:    recordIDSetSHA,
		MutationLogWatermark: wPtr,
		FencingToken:         fencingToken,
	}, nil
}

// ListSnapshots returns every retained snapshot for workspaceID ordered by
// timestamp ascending (oldest first).
func (s *Store) ListSnapshots(ctx context.Context, workspaceID string) ([]Snapshot, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT snapshot_id FROM snapshots WHERE workspace_id = ? ORDER BY timestamp ASC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(ids))
	for _, id := range ids {
		snap, err := s.GetSnapshot(ctx, workspaceID, id)
		if err != nil {
			return nil, err
		}
		if snap != nil {
			out = append(out, *snap)
		}
	}
	return out, nil
}

// flattenSnapshotContent turns a §1.1 content map (kind -> record_id ->
// body) into replay's flat record_id -> body working-state shape,
// alongside a parallel record_id -> kind map recovered for reconstruction.
func flattenSnapshotContent(content map[string]map[string]map[string]any) (map[string]map[string]any, map[string]RecordKind) {
	state := make(map[string]map[string]any)
	kinds := make(map[string]RecordKind)
	for kind, records := range content {
		for recordID, body := range records {
			state[recordID] = body
			kinds[recordID] = RecordKind(kind)
		}
	}
	return state, kinds
}
