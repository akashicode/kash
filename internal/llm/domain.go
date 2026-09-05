package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DomainEvidence is what the model is shown about a corpus: measurements
// already taken, plus a sample of the text.
type DomainEvidence struct {
	// FoldDiacritics is the detected script mode.
	FoldDiacritics string
	// NumberingLabels are the numbering units detection found, e.g. "dhāraṇā".
	NumberingLabels []string
	// TitleTokens are recurring words in document titles.
	TitleTokens []string
	// HonorificCandidates are leading title words mined from entity-like
	// phrases. The model may only filter these, never add to them.
	HonorificCandidates []string
	// DefaultPredicates is the built-in vocabulary the result is unioned with.
	DefaultPredicates []string
	// Sample is representative corpus text.
	Sample string
}

// DomainSuggestion is the model's contribution to a corpus profile.
type DomainSuggestion struct {
	Predicates           []string          `json:"predicates"`
	Priorities           []string          `json:"priorities"`
	Honorifics           []string          `json:"honorifics"`
	ProperNounPredicates []string          `json:"proper_noun_predicates"`
	MetaKeys             map[string]string `json:"meta_keys"`
}

// maxPredicates bounds the closed extraction vocabulary. A long list stops
// constraining the extractor, which is the only reason it is closed.
const maxPredicates = 24

// SuggestDomainConfig asks the model for the parts of a corpus profile that are
// genuine judgment rather than measurement.
//
// The model is never asked for a regular expression, a number or a boolean. It
// returns words and picks from lists it was given. That is a security boundary,
// not a style preference: the sample is untrusted corpus text and the output
// becomes configuration that runs against every query, so a document must not
// be able to inject a pattern. The caller enforces the same rule again when it
// validates the result.
func (c *Client) SuggestDomainConfig(ctx context.Context, ev DomainEvidence) (DomainSuggestion, error) {
	var b strings.Builder

	b.WriteString("Corpus evidence already measured:\n")
	fmt.Fprintf(&b, "- script: %s\n", ev.FoldDiacritics)
	if len(ev.NumberingLabels) > 0 {
		fmt.Fprintf(&b, "- documents number their sections as: %s\n", strings.Join(ev.NumberingLabels, ", "))
	}
	if len(ev.TitleTokens) > 0 {
		fmt.Fprintf(&b, "- words recurring across document titles: %s\n", strings.Join(ev.TitleTokens, ", "))
	}
	if len(ev.HonorificCandidates) > 0 {
		fmt.Fprintf(&b, "- candidate honorifics found in the text: %s\n", strings.Join(ev.HonorificCandidates, ", "))
	}
	fmt.Fprintf(&b, "\nGeneric predicate vocabulary to extend:\n%s\n", strings.Join(ev.DefaultPredicates, ", "))
	fmt.Fprintf(&b, "\nSample of the corpus:\n%s\n", ev.Sample)

	system := `You configure a knowledge-graph extractor for one specific document corpus.

Return ONLY a JSON object, no markdown fences, no explanation:
{
  "predicates": ["<relation phrase>", ...],
  "priorities": ["<what to favour, one sentence>", ...],
  "honorifics": ["<title >", ...],
  "proper_noun_predicates": ["<predicate>", ...],
  "meta_keys": {"<numbering label>": "<short ascii key>"}
}

RULES:
- predicates: relation phrases this corpus actually contains, beyond the generic
  list. Lowercase, 1-4 words, in the form "was teacher of", "commented on".
  These EXTEND the generic vocabulary; do not repeat entries it already has.
- priorities: at most 3 sentences naming which relations matter most here.
- honorifics: choose ONLY from the candidate list supplied above. Keep the ones
  that are titles or forms of address rather than parts of a name. Lowercase,
  with a trailing space. Return [] if none qualify.
- proper_noun_predicates: the subset of predicates whose object is a person,
  work or product.
- meta_keys: for each numbering label above, a short lowercase ASCII key a
  reader would type — "dhāraṇā" becomes "dharana". The label "paren" is not a
  word from the corpus: it marks passages numbered as a bare "48)", with no
  unit named beside the number. Give it the word this corpus uses for those
  numbered passages elsewhere — the same key as that label — so one reference
  is not filed under two names. If the corpus names no such unit, answer
  "section".
- Never invent a regular expression, a number, or a true/false value.`

	raw, err := c.Complete(ctx, system, b.String())
	if err != nil {
		return DomainSuggestion{}, fmt.Errorf("suggest domain config: %w", err)
	}

	body, err := extractJSONObject(raw)
	if err != nil {
		return DomainSuggestion{}, fmt.Errorf("parse domain suggestion: %w", err)
	}

	var s DomainSuggestion
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		return DomainSuggestion{}, fmt.Errorf("unmarshal domain suggestion: %w", err)
	}

	s.Predicates = cleanStringSlice(s.Predicates)
	s.Priorities = cleanStringSlice(s.Priorities)
	s.Honorifics = cleanStringSlice(s.Honorifics)
	s.ProperNounPredicates = cleanStringSlice(s.ProperNounPredicates)

	return s, nil
}

// MergePredicates unions suggested predicates with the defaults.
//
// It is a union and never a replacement. The vocabulary is closed and a fact
// matching no predicate is dropped, so losing a default silently discards a
// whole class of relations with nothing to indicate it happened.
func MergePredicates(defaults, suggested []string) []string {
	out := append([]string{}, defaults...)
	seen := map[string]bool{}
	for _, p := range out {
		seen[strings.ToLower(p)] = true
	}
	for _, p := range suggested {
		key := strings.ToLower(strings.TrimSpace(p))
		if key == "" || seen[key] || len(out) >= maxPredicates {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// FilterToCandidates keeps only values the model was offered, and returns them
// in the exact form the candidate list carried.
//
// The model may filter a mined list but never extend it. Without this an
// adversarial or merely odd document could introduce arbitrary strings into
// configuration by way of the sample.
//
// Returning the candidate rather than the model's echo of it is what makes the
// value usable. Honorifics carry a trailing space because they are stripped
// with a plain prefix cut, and the model's reply has already been trimmed by
// the time it arrives here — echoing it back would turn "śrī " into "śrī" and
// strip that prefix out of every word beginning with those letters.
func FilterToCandidates(suggested, candidates []string) []string {
	canonical := make(map[string]string, len(candidates))
	for _, c := range candidates {
		canonical[matchKey(c)] = c
	}

	var out []string
	seen := map[string]bool{}
	for _, s := range suggested {
		key := matchKey(s)
		c, ok := canonical[key]
		if key == "" || seen[key] || !ok {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// SubsetOf keeps only values present in the allowed set, returning the allowed
// spelling so the result is byte-identical to the list it is a subset of.
func SubsetOf(values, allowed []string) []string {
	canonical := make(map[string]string, len(allowed))
	for _, a := range allowed {
		canonical[matchKey(a)] = a
	}
	var out []string
	for _, v := range values {
		if a, ok := canonical[matchKey(v)]; ok {
			out = append(out, a)
		}
	}
	return out
}

// matchKey compares model output to a supplied list without letting whitespace
// or case decide membership.
func matchKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
