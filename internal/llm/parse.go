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

	// Find JSON array boundaries.
	//
	// "No facts here" and "the response was unusable" must not look alike. An
	// empty result is checkpointed as a completed batch and never revisited, so
	// treating a truncated or refused response as zero facts silently discards
	// that batch's passages from the graph forever. An explicitly empty array
	// is the only response that means no facts.
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	switch {
	case start == -1 && strings.Contains(raw, "]"):
		return nil, fmt.Errorf("malformed response: closing bracket without opening")
	case start >= 0 && end < start:
		return nil, fmt.Errorf("truncated response: array opened at %d but never closed", start)
	case start == -1 || end == -1:
		if raw == "" {
			return nil, fmt.Errorf("empty response")
		}
		return nil, fmt.Errorf("no JSON array in response: %.120q", raw)
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

// parseDecomposedQuery parses a JSON object with specific_entities and broad_concepts.
// It is lenient with markdown fences and surrounding explanatory text.
func parseDecomposedQuery(raw string) (DecomposedQuery, error) {
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

	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		if raw == "" {
			return DecomposedQuery{}, fmt.Errorf("empty response")
		}
		return DecomposedQuery{}, fmt.Errorf("no JSON object in response: %.120q", raw)
	}
	raw = raw[start : end+1]

	var dq DecomposedQuery
	if err := json.Unmarshal([]byte(raw), &dq); err != nil {
		return DecomposedQuery{}, fmt.Errorf("unmarshal decomposed query JSON: %w", err)
	}

	dq.SpecificEntities = cleanStringSlice(dq.SpecificEntities)
	dq.BroadConcepts = cleanStringSlice(dq.BroadConcepts)

	return dq, nil
}

func cleanStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		lower := strings.ToLower(s)
		if !seen[lower] {
			seen[lower] = true
			out = append(out, s)
		}
	}
	return out
}
