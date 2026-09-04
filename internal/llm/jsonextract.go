package llm

import (
	"fmt"
	"strings"
)

// Shared JSON extraction for model responses.
//
// Every structured call in this package is Complete() plus a lenient parser,
// and each one had reimplemented the same fence-stripping and brace-finding
// prelude. Collecting it here keeps one behaviour — in particular the
// distinction between "the model said nothing" and "the response was
// unusable", which several of the copies had flattened into a single error.

// stripFences removes a leading markdown code fence and its trailing partner.
func stripFences(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "```") {
		return raw
	}
	if lines := strings.SplitN(raw, "\n", 2); len(lines) > 1 {
		raw = lines[1]
	}
	return strings.TrimSpace(strings.TrimSuffix(raw, "```"))
}

// extractJSON returns the outermost JSON value delimited by open/close.
//
// The error cases are deliberately distinct. A caller that checkpoints an empty
// result must be able to tell "no data" from "unreadable", because treating a
// truncated response as an empty one discards work permanently and silently.
func extractJSON(raw string, open, close byte) (string, error) {
	raw = stripFences(raw)
	if raw == "" {
		return "", fmt.Errorf("empty response")
	}

	start := strings.IndexByte(raw, open)
	end := strings.LastIndexByte(raw, close)

	switch {
	case start == -1 && strings.IndexByte(raw, close) >= 0:
		return "", fmt.Errorf("malformed response: closing %q without opening %q", close, open)
	case start >= 0 && end < start:
		return "", fmt.Errorf("truncated response: %q opened at %d but never closed", open, start)
	case start == -1 || end == -1:
		return "", fmt.Errorf("no JSON %q...%q in response: %.120q", open, close, raw)
	}
	return raw[start : end+1], nil
}

// extractJSONArray returns the outermost JSON array in a response.
func extractJSONArray(raw string) (string, error) { return extractJSON(raw, '[', ']') }

// extractJSONObject returns the outermost JSON object in a response.
func extractJSONObject(raw string) (string, error) { return extractJSON(raw, '{', '}') }
