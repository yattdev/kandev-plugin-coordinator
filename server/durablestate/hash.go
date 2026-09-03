package durablestate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// canonicalHash mirrors replay_reference.py's canonical_hash: sha256 over a
// canonical JSON serialization of a record body — sort_keys=True, no extra
// whitespace, UTF-8. Go's encoding/json already sorts map[string]any keys
// lexicographically and emits no extra whitespace by default, so
// json.Marshal on a map[string]any / normalized value is byte-identical to
// Python's json.dumps(obj, sort_keys=True, separators=(",", ":")) for the
// JSON types both languages share (the caller is responsible for only
// storing JSON-representable bodies: strings, float64/int, bool, nil, and
// nested maps/slices of the same).
func canonicalHash(body map[string]any) (string, error) {
	normalized, err := normalizeJSONValue(body)
	if err != nil {
		return "", fmt.Errorf("durablestate: canonicalizing body: %w", err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("durablestate: marshaling canonical body: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum), nil
}

// normalizeJSONValue round-trips a value through JSON encode/decode so that
// any Go-side numeric type (int, int64, float64, ...) collapses to the same
// float64/json.Number representation json.Unmarshal would have produced had
// this body arrived over the wire, keeping hashes stable regardless of how
// callers constructed the body in memory.
func normalizeJSONValue(v any) (any, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// canonicalHashBytes is canonicalHash's counterpart for arbitrary
// already-serialized-elsewhere byte payloads (used for archive_append's
// sha256_of_appended_bytes, which hashes the exact bytes appended, not a
// re-derived body).
func canonicalHashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)
}

// recordIDSetSHA256 mirrors replay_reference.py's record_id_set_sha256 and
// CONTRACT_MAPPING.md §6's digest convention: sort the record IDs, join
// with "\n", sha256 the UTF-8 bytes.
func recordIDSetSHA256(recordIDs []string) string {
	sorted := append([]string(nil), recordIDs...)
	sort.Strings(sorted)
	joined := strings.Join(sorted, "\n")
	sum := sha256.Sum256([]byte(joined))
	return fmt.Sprintf("%x", sum)
}

// byteCountOf returns the canonical-serialization byte length used for
// byte_count fields throughout the spec (§1.1, §3).
func byteCountOf(body map[string]any) (int, error) {
	normalized, err := normalizeJSONValue(body)
	if err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}
