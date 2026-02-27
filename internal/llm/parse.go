package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseTriples parses a JSON array of triple objects from an LLM response.
// It is lenient and tries to extract JSON even if surrounded by markdown fences.
func parseTriples(raw string) ([]Triple, error) {
	raw = strings.TrimSpace(raw)

	// Strip markdown code fences if present
	if strings.HasPrefix(raw, "```") {
		lines := strings.SplitN(raw, "\n", 2)
		if len(lines) > 1 {
			raw = lines[1]
		}
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	}

	// Find JSON array boundaries
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start == -1 || end == -1 || end < start {
		// No JSON array found; return empty rather than error
		return []Triple{}, nil
	}
	raw = raw[start : end+1]

	var triples []Triple
	if err := json.Unmarshal([]byte(raw), &triples); err != nil {
		return nil, fmt.Errorf("unmarshal triples JSON: %w", err)
	}

	// Filter out empty triples
	filtered := make([]Triple, 0, len(triples))
	for _, t := range triples {
		if t.Subject != "" && t.Predicate != "" && t.Object != "" {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}
