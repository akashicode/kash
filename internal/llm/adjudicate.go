package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// EntityGroup is one candidate merge presented for adjudication: several
// surface forms that a deterministic pass thinks may name the same entity.
type EntityGroup struct {
	// Key identifies the group so verdicts can be matched back.
	Key string
	// Canonical is the proposed canonical form.
	Canonical string
	// Aliases are the other surface forms in the group.
	Aliases []string
	// Context holds a few sample relations per form ("authored Tantraloka"),
	// which is what lets the model tell brahma from brahmā.
	Context []string
}

// EntityVerdict is the model's decision on one group.
type EntityVerdict struct {
	Key        string `json:"key"`
	SameEntity bool   `json:"same_entity"`
	Reason     string `json:"reason"`
}

// AdjudicateEntities asks the LLM whether each group of surface forms names a
// single entity. It is used only for candidates that deterministic rules
// cannot settle — chiefly forms differing by meaning-bearing diacritics.
//
// Groups should be batched by the caller; a batch of ~15 keeps the prompt
// small and the responses reliable.
func (c *Client) AdjudicateEntities(ctx context.Context, groups []EntityGroup) ([]EntityVerdict, error) {
	if len(groups) == 0 {
		return nil, nil
	}

	system := `You decide whether different spellings refer to the SAME entity or to DIFFERENT entities.

Each group below lists surface forms found in a document corpus, with sample relations for context.

Answer SAME only when the forms are genuinely the same thing written differently:
transliteration variants, spelling variants, abbreviations, or a title attached
to a name ("Dr. Karman" and "Karman").

Answer DIFFERENT when the forms are distinct words or distinct entities that merely
look similar. Be careful with languages where diacritics are meaning-bearing —
in Sanskrit, brahma (the absolute) and brahmā (the creator god) are different,
as are dhanya (blessed) and dhānya (grain). The sample relations are your best
evidence: forms used in unrelated ways are usually different entities.

When uncertain, answer DIFFERENT. A wrong merge silently fabricates connections
in the knowledge graph; a missed merge only leaves it as it already is.

Return ONLY a JSON array, one object per group, no markdown fences:
[{"key": "<the group key>", "same_entity": true, "reason": "<8 words max>"}]`

	var b strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&b, "\nGROUP key=%q\n  forms: %s\n", g.Key,
			strings.Join(append([]string{g.Canonical}, g.Aliases...), " | "))
		for _, ctxLine := range g.Context {
			fmt.Fprintf(&b, "  context: %s\n", ctxLine)
		}
	}

	raw, err := c.Complete(ctx, system, "Decide for each group:\n"+b.String())
	if err != nil {
		return nil, fmt.Errorf("adjudicate entities: %w", err)
	}

	verdicts, err := parseVerdicts(raw)
	if err != nil {
		return nil, fmt.Errorf("parse adjudication response: %w", err)
	}
	return verdicts, nil
}

// parseVerdicts extracts the JSON verdict array, tolerating markdown fences
// and surrounding prose.
func parseVerdicts(raw string) ([]EntityVerdict, error) {
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

	var verdicts []EntityVerdict
	if err := json.Unmarshal([]byte(raw[start:end+1]), &verdicts); err != nil {
		return nil, fmt.Errorf("unmarshal verdicts: %w", err)
	}

	filtered := make([]EntityVerdict, 0, len(verdicts))
	for _, v := range verdicts {
		if v.Key != "" {
			filtered = append(filtered, v)
		}
	}
	return filtered, nil
}
