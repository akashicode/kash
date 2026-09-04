package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// EntityToDescribe bundles an entity and its graph facts for summarization.
type EntityToDescribe struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	Facts   []string `json:"facts"`
}

// EntityDescriptionResult is the generated description for an entity.
type EntityDescriptionResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DeterministicDescription produces a factual fallback description when
// the LLM is unavailable or an API call fails.
func DeterministicDescription(name string, facts []string) string {
	name = strings.TrimSpace(name)
	if len(facts) == 0 {
		return name
	}
	limit := 4
	if len(facts) > limit {
		facts = facts[:limit]
	}
	return fmt.Sprintf("Entity associated with: %s.", strings.Join(facts, "; "))
}

// DescribeEntities batches multiple entities and asks the LLM to generate
// concise, factual 1-2 sentence descriptions for each, grounded strictly
// in the provided graph facts.
func (c *Client) DescribeEntities(ctx context.Context, entities []EntityToDescribe) ([]EntityDescriptionResult, error) {
	if len(entities) == 0 {
		return nil, nil
	}

	system := `You are a knowledge graph summarizer.
For each entity below, write a concise, factual 1-2 sentence description summarizing what it is and its key relationships based ONLY on the provided facts.
Do not invent information. Do not assume or extrapolate. If facts are sparse, state only what is known.

Return ONLY a valid JSON array, no markdown fences, one object per entity:
[{"name": "<entity name>", "description": "<1-2 sentences>"}]`

	var b strings.Builder
	for _, e := range entities {
		fmt.Fprintf(&b, "\nENTITY: %s\n", e.Name)
		if len(e.Aliases) > 0 {
			fmt.Fprintf(&b, "ALIASES: %s\n", strings.Join(e.Aliases, ", "))
		}
		fmt.Fprintf(&b, "FACTS:\n")
		for _, f := range e.Facts {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}

	raw, err := c.Complete(ctx, system, "Summarize each entity:\n"+b.String())
	if err != nil {
		return nil, fmt.Errorf("describe entities: %w", err)
	}

	results, err := parseDescriptions(raw)
	if err != nil {
		return nil, fmt.Errorf("parse descriptions: %w", err)
	}
	return results, nil
}

// parseDescriptions extracts the JSON description array, tolerating markdown fences
// and surrounding prose.
func parseDescriptions(raw string) ([]EntityDescriptionResult, error) {
	raw = strings.TrimSpace(raw)

	if strings.HasPrefix(raw, "```") {
		if _, rest, ok := strings.Cut(raw, "\n"); ok {
			raw = rest
		}
		raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "```"))
	}

	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start == -1 || end == -1 || end < start {
		return nil, errors.New("no JSON array in response")
	}

	var results []EntityDescriptionResult
	if err := json.Unmarshal([]byte(raw[start:end+1]), &results); err != nil {
		return nil, fmt.Errorf("unmarshal descriptions: %w", err)
	}

	filtered := make([]EntityDescriptionResult, 0, len(results))
	for _, r := range results {
		r.Name = strings.TrimSpace(r.Name)
		r.Description = strings.TrimSpace(r.Description)
		if r.Name != "" && r.Description != "" {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}
