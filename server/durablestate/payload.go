package durablestate

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ErrContentRefUnavailable is returned when a content_ref cannot be
// resolved (pruned, missing, or the payload store has no such locator).
// §1.3: replay/reads must abort as corrupt input, never silently skip.
var ErrContentRefUnavailable = fmt.Errorf("durablestate: content_ref is not available (pruned, missing, or no payload store entry)")

// ErrHashMismatch is returned when a resolved body's recomputed hash does
// not match its declared sha256 (corrupt or substituted content).
var ErrHashMismatch = fmt.Errorf("durablestate: resolved body hash does not match declared sha256")

func marshalBody(body map[string]any) (string, error) {
	encoded, err := canonicalJSONBytes(body)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func unmarshalBody(s string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(s)))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	out, err := restoreJSONNumbers(raw)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func restoreJSONNumbers(v any) (any, error) {
	switch val := v.(type) {
	case json.Number:
		text := val.String()
		if strings.ContainsAny(text, ".eE") {
			f, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, err
			}
			return f, nil
		}
		i, err := strconv.ParseInt(text, 10, 64)
		if err == nil {
			return i, nil
		}
		u, err := strconv.ParseUint(text, 10, 64)
		if err == nil {
			return u, nil
		}
		return nil, fmt.Errorf("durablestate: invalid JSON number %q", text)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			restored, err := restoreJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			out[i] = restored
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			restored, err := restoreJSONNumbers(item)
			if err != nil {
				return nil, err
			}
			out[k] = restored
		}
		return out, nil
	default:
		return val, nil
	}
}

// putContentStoreRef stores body under its content-address (sha256:<hex>)
// in this workspace's content_store, returning the ref locator. Idempotent:
// re-putting identical content is a no-op (content-addressed storage is
// naturally deduplicated).
func putContentStoreRef(ctx context.Context, tx execer, workspaceID string, body map[string]any) (ref string, sha string, err error) {
	sha, err = canonicalHash(body)
	if err != nil {
		return "", "", err
	}
	encoded, err := marshalBody(body)
	if err != nil {
		return "", "", err
	}
	ref = "content:" + sha
	_, err = tx.ExecContext(ctx,
		`INSERT INTO content_store (workspace_id, sha256, body, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (workspace_id, sha256) DO NOTHING`,
		workspaceID, sha, encoded, nowUTC())
	if err != nil {
		return "", "", err
	}
	return ref, sha, nil
}

// resolveContentStoreRef dereferences a "content:<sha256>" ref.
func resolveContentStoreRef(ctx context.Context, tx execer, workspaceID, sha string) (map[string]any, error) {
	var encoded string
	err := tx.QueryRowContext(ctx,
		`SELECT body FROM content_store WHERE workspace_id = ? AND sha256 = ?`, workspaceID, sha,
	).Scan(&encoded)
	if err == sql.ErrNoRows {
		return nil, ErrContentRefUnavailable
	}
	if err != nil {
		return nil, err
	}
	return unmarshalBody(encoded)
}

// resolveArchiveRef dereferences an "archive:<compaction_id>:<record_id>"
// ref against the archive table (the exact archive-append location a
// rollup already wrote, per §1.3's guidance for large removed bodies).
func resolveArchiveRef(ctx context.Context, tx execer, workspaceID, compactionID, recordID string) (map[string]any, error) {
	var encoded string
	err := tx.QueryRowContext(ctx,
		`SELECT body FROM archive WHERE workspace_id = ? AND compaction_id = ? AND record_id = ?`,
		workspaceID, compactionID, recordID,
	).Scan(&encoded)
	if err == sql.ErrNoRows {
		return nil, ErrContentRefUnavailable
	}
	if err != nil {
		return nil, err
	}
	return unmarshalBody(encoded)
}

// resolvePayload mirrors replay_reference.py's resolve_payload: resolves a
// PayloadSide to its body, verifying the resolved body's hash against the
// declared sha256. Returns nil, nil for a nil side (add's before / remove's
// after).
func resolvePayload(ctx context.Context, tx execer, workspaceID string, side *PayloadSide) (map[string]any, error) {
	if side == nil {
		return nil, nil
	}
	var body map[string]any
	var err error
	switch side.Storage {
	case StorageInline:
		body = side.Body
	case StorageContentRef:
		body, err = resolvePayloadRef(ctx, tx, workspaceID, side.Ref)
	default:
		return nil, fmt.Errorf("durablestate: unknown storage kind %q", side.Storage)
	}
	if err != nil {
		return nil, err
	}
	recomputed, err := canonicalHash(body)
	if err != nil {
		return nil, err
	}
	if recomputed != side.SHA256 {
		return nil, fmt.Errorf("%w: recomputed=%s declared=%s", ErrHashMismatch, recomputed, side.SHA256)
	}
	return body, nil
}

// resolvePayloadRef dispatches a ref locator to the right backing store
// based on its prefix ("content:<sha256>" or "archive:<compaction_id>:
// <record_id>").
func resolvePayloadRef(ctx context.Context, tx execer, workspaceID, ref string) (map[string]any, error) {
	switch {
	case len(ref) > len("content:") && ref[:len("content:")] == "content:":
		return resolveContentStoreRef(ctx, tx, workspaceID, ref[len("content:"):])
	case len(ref) > len("archive:") && ref[:len("archive:")] == "archive:":
		rest := ref[len("archive:"):]
		for i := 0; i < len(rest); i++ {
			if rest[i] == ':' {
				return resolveArchiveRef(ctx, tx, workspaceID, rest[:i], rest[i+1:])
			}
		}
		return nil, fmt.Errorf("durablestate: malformed archive ref %q", ref)
	default:
		return nil, fmt.Errorf("durablestate: unrecognized content_ref locator %q", ref)
	}
}
