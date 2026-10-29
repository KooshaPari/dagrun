package harness

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// ParseUsage extracts token accounting from a harness's stdout.
//
// A shape that is not recognised returns (nil, nil): a missing number must
// never become a fabricated zero.
func ParseUsage(data []byte) (*Usage, error) {
	obj, looksJSON, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	if obj == nil || !looksJSON {
		return nil, nil
	}
	return usageFromObject(obj), nil
}

// ParseUsageNDJSON extracts usage from a codex-style JSONL event stream,
// taking the LAST usage-looking object in the stream.
func ParseUsageNDJSON(data []byte) (*Usage, error) {
	var last *Usage
	var firstErr error

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		u, err := ParseUsage(line)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if u != nil {
			last = u
		}
	}

	if last != nil {
		return last, nil
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

// ParseUsageStream tries NDJSON first (when the bytes contain more than one
// JSON object) then the single-object form.
func ParseUsageStream(data []byte) (*Usage, error) {
	if countJSONLines(data) > 1 {
		return ParseUsageNDJSON(data)
	}
	return ParseUsage(data)
}

// countJSONLines counts non-empty lines that begin with a JSON object.
func countJSONLines(data []byte) int {
	n := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 && line[0] == '{' {
			n++
		}
	}
	return n
}

// decodeObject unmarshals data as a single JSON object. looksJSON reports
// whether the bytes were shaped like JSON at all, so a parse failure can be
// distinguished from plain non-JSON output.
func decodeObject(data []byte) (obj map[string]any, looksJSON bool, err error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return nil, false, nil
	}

	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return nil, true, err
	}

	m, ok := v.(map[string]any)
	if !ok {
		// Valid JSON, but not an object we know how to read.
		return nil, true, nil
	}
	return m, true, nil
}

// usageFromObject maps a recognised harness object onto Usage. It returns nil
// when the object carries no token accounting at all.
func usageFromObject(obj map[string]any) *Usage {
	u := &Usage{}
	found := false

	if s, ok := stringField(obj, "provider"); ok {
		u.Provider = s
	}
	if s, ok := stringField(obj, "model"); ok {
		u.Model = s
	}

	// jcode/kcode and codex stream events: {"usage": {...}}
	if usage, ok := objectField(obj, "usage"); ok {
		if applyTokenFields(u, usage) {
			found = true
		}
	}

	// codex session info: {"info": {"total_token_usage": {...}}}
	if info, ok := objectField(obj, "info"); ok {
		if totals, ok := objectField(info, "total_token_usage"); ok {
			if applyTokenFields(u, totals) {
				found = true
			}
		}
	}

	// A bare usage object: {"input_tokens": N, "output_tokens": N}
	if !found {
		if applyTokenFields(u, obj) {
			found = true
		}
	}

	if !found {
		return nil
	}
	return u
}

// applyTokenFields copies every recognised token field present in m onto u,
// reporting whether anything was found.
func applyTokenFields(u *Usage, m map[string]any) bool {
	found := false

	if n, ok := intField(m, "input_tokens"); ok {
		u.InputTokens = n
		found = true
	}
	if n, ok := intField(m, "output_tokens"); ok {
		u.OutputTokens = n
		found = true
	}
	// codex names the cache read counter "cached_input_tokens".
	if n, ok := intField(m, "cached_input_tokens"); ok {
		u.CacheReadTokens = n
		found = true
	}
	if n, ok := intField(m, "cache_read_input_tokens"); ok {
		u.CacheReadTokens = n
		found = true
	}
	if n, ok := intField(m, "cache_creation_input_tokens"); ok {
		u.CacheWriteTokens = n
		found = true
	}
	if n, ok := intField(m, "total_tokens"); ok {
		u.TotalTokens = n
		found = true
	}
	if n, ok := intField(m, "duration_ms"); ok {
		u.DurationMS = int64(n)
		found = true
	}

	return found
}

func objectField(obj map[string]any, key string) (map[string]any, bool) {
	v, ok := obj[key]
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	return m, true
}

func stringField(obj map[string]any, key string) (string, bool) {
	v, ok := obj[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func intField(obj map[string]any, key string) (int, bool) {
	v, ok := obj[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case string:
		// Some harnesses stringify counters.
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}
