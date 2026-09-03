package durablestate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// canonicalHash mirrors replay_reference.py's canonical_hash: sha256 over a
// canonical JSON serialization of a record body — sort_keys=True, no extra
// whitespace, UTF-8, via Python's json.dumps(obj, sort_keys=True,
// separators=(",", ":")) with its default ensure_ascii=True. That is NOT
// byte-identical to Go's encoding/json.Marshal: Go unconditionally
// HTML-escapes '<', '>', and '&' (which Python never escapes) and leaves
// non-ASCII runes as raw UTF-8 (which Python's ensure_ascii=True instead
// escapes to \uXXXX, with surrogate pairs above U+FFFF). canonicalJSONBytes
// below implements Python's exact escaping rules instead of delegating to
// json.Marshal for the final byte output.
func canonicalHash(body map[string]any) (string, error) {
	encoded, err := canonicalJSONBytes(body)
	if err != nil {
		return "", fmt.Errorf("durablestate: canonicalizing body: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum), nil
}

// canonicalJSONBytes serializes v exactly like Python's
// json.dumps(v, sort_keys=True, separators=(",", ":")) (default
// ensure_ascii=True): object keys sorted, no extra whitespace, and string
// escaping limited to '"', '\\', control characters (U+0000-U+001F and
// U+007F), and every code point at or above U+0080 (escaped to \uXXXX, with
// UTF-16 surrogate pairs above U+FFFF) — never '<', '>', or '&'. v is first
// normalized through normalizeJSONValue so any Go-side numeric type
// collapses to the same float64 representation json.Unmarshal would have
// produced.
func canonicalJSONBytes(v any) ([]byte, error) {
	normalized, err := normalizeJSONValue(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonicalJSON(&buf, normalized); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonicalJSON(buf *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case float64:
		// Number formatting itself is unchanged from prior behavior
		// (delegated to encoding/json); only the surrounding structural/
		// string encoding changes here.
		encoded, err := json.Marshal(val)
		if err != nil {
			return err
		}
		buf.Write(encoded)
	case string:
		writeCanonicalString(buf, val)
	case []any:
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalJSON(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		buf.WriteByte('{')
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonicalString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonicalJSON(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("durablestate: canonical JSON: unsupported type %T", v)
	}
	return nil
}

const canonicalHexDigits = "0123456789abcdef"

// writeCanonicalString writes s exactly as Python's json module would with
// ensure_ascii=True: '"' and '\\' are backslash-escaped; '\b','\f','\n',
// '\r','\t' use their short escapes; every other control character
// (U+0000-U+001F) and U+007F escape as \u00XX; every code point >= U+0080
// escapes as \uXXXX (surrogate pair above U+FFFF); everything else
// (U+0020-U+007E, including '<', '>', '&', '/', '\”) is written literally.
func writeCanonicalString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			buf.WriteString(`\"`)
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r < 0x20 || r == 0x7f:
			writeUnicodeEscape(buf, uint16(r))
		case r < 0x80:
			buf.WriteByte(byte(r))
		case r <= 0xFFFF:
			writeUnicodeEscape(buf, uint16(r))
		default:
			r -= 0x10000
			hi := uint16(0xD800 + (r >> 10))
			lo := uint16(0xDC00 + (r & 0x3FF))
			writeUnicodeEscape(buf, hi)
			writeUnicodeEscape(buf, lo)
		}
	}
	buf.WriteByte('"')
}

func writeUnicodeEscape(buf *bytes.Buffer, v uint16) {
	buf.WriteString(`\u`)
	buf.WriteByte(canonicalHexDigits[(v>>12)&0xF])
	buf.WriteByte(canonicalHexDigits[(v>>8)&0xF])
	buf.WriteByte(canonicalHexDigits[(v>>4)&0xF])
	buf.WriteByte(canonicalHexDigits[v&0xF])
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
	encoded, err := canonicalJSONBytes(body)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}
