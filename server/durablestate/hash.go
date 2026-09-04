package durablestate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
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
// normalized through normalizeJSONValue so composite Go values have
// JSON-compatible shapes and numeric values are emitted with Python's
// observable int/float spelling.
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
		encoded, err := pythonFloatBytes(val)
		if err != nil {
			return err
		}
		buf.Write(encoded)
	case canonicalNumber:
		buf.WriteString(val.text)
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

type canonicalNumber struct {
	text string
}

func pythonFloatBytes(f float64) ([]byte, error) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, fmt.Errorf("durablestate: canonical JSON: unsupported non-finite float %v", f)
	}
	var out string
	abs := math.Abs(f)
	switch {
	case f == 0:
		if math.Signbit(f) {
			out = "-0.0"
		} else {
			out = "0.0"
		}
	case abs >= 1e-4 && abs < 1e16:
		out = strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(out, ".") {
			out += ".0"
		}
	default:
		out = strconv.FormatFloat(f, 'e', -1, 64)
	}
	return []byte(out), nil
}

// normalizeJSONValue round-trips a value through JSON encode/decode so that
// composite types have JSON-compatible shapes, while preserving Python's
// observable int-vs-float serialization distinction for typed Go numeric
// values. json.Number values are emitted according to their original JSON
// lexical category: integer tokens stay integers; tokens containing a
// decimal point or exponent are formatted as Python floats.
func normalizeJSONValue(v any) (any, error) {
	switch val := v.(type) {
	case nil, bool, string:
		return val, nil
	case json.Number:
		return normalizeJSONNumber(val)
	case float32:
		return float64(val), nil
	case float64:
		return val, nil
	case int:
		return canonicalNumber{text: strconv.FormatInt(int64(val), 10)}, nil
	case int8:
		return canonicalNumber{text: strconv.FormatInt(int64(val), 10)}, nil
	case int16:
		return canonicalNumber{text: strconv.FormatInt(int64(val), 10)}, nil
	case int32:
		return canonicalNumber{text: strconv.FormatInt(int64(val), 10)}, nil
	case int64:
		return canonicalNumber{text: strconv.FormatInt(val, 10)}, nil
	case uint:
		return canonicalNumber{text: strconv.FormatUint(uint64(val), 10)}, nil
	case uint8:
		return canonicalNumber{text: strconv.FormatUint(uint64(val), 10)}, nil
	case uint16:
		return canonicalNumber{text: strconv.FormatUint(uint64(val), 10)}, nil
	case uint32:
		return canonicalNumber{text: strconv.FormatUint(uint64(val), 10)}, nil
	case uint64:
		return canonicalNumber{text: strconv.FormatUint(val, 10)}, nil
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			out[k] = normalized
		}
		return out, nil
	default:
		rv := reflect.ValueOf(v)
		if rv.IsValid() && rv.Kind() == reflect.Slice {
			out := make([]any, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				normalized, err := normalizeJSONValue(rv.Index(i).Interface())
				if err != nil {
					return nil, err
				}
				out[i] = normalized
			}
			return out, nil
		}
		if rv.IsValid() && rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String {
			out := make(map[string]any, rv.Len())
			iter := rv.MapRange()
			for iter.Next() {
				normalized, err := normalizeJSONValue(iter.Value().Interface())
				if err != nil {
					return nil, err
				}
				out[iter.Key().String()] = normalized
			}
			return out, nil
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var out any
		if err := decoder.Decode(&out); err != nil {
			return nil, err
		}
		return normalizeJSONValue(out)
	}
}

func normalizeJSONNumber(n json.Number) (any, error) {
	text := n.String()
	if strings.ContainsAny(text, ".eE") {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("durablestate: canonical JSON: invalid JSON number %q", text)
		}
		return f, nil
	}
	if _, err := strconv.ParseInt(text, 10, 64); err == nil {
		return canonicalNumber{text: text}, nil
	}
	if _, err := strconv.ParseUint(text, 10, 64); err == nil {
		return canonicalNumber{text: text}, nil
	}
	return nil, fmt.Errorf("durablestate: canonical JSON: invalid JSON integer %q", text)
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
