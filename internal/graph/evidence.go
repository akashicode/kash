package graph

import (
	"strings"

	"github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/lexical"
)

// Evidence says how strongly a passage supports a triple.
type Evidence int

const (
	// EvidenceNone means neither endpoint of the triple appears in the passage.
	EvidenceNone Evidence = iota
	// EvidencePartial means one endpoint appears.
	EvidencePartial
	// EvidenceBoth means both endpoints appear, which is the most a lexical
	// check can establish. It does not confirm the predicate: only that the
	// passage is talking about both things the fact relates.
	EvidenceBoth
)

func (e Evidence) String() string {
	switch e {
	case EvidenceBoth:
		return "both"
	case EvidencePartial:
		return "partial"
	default:
		return "none"
	}
}

// minEvidenceToken is the shortest token that counts toward evidence. Shorter
// ones ("of", "the", a stray initial) match everywhere and prove nothing.
const minEvidenceToken = 3

// EvidenceChecker tests whether a passage mentions the endpoints of a triple.
//
// It exists because a triple's chunk id is a claim about where a fact came
// from, and a claim nobody checks is not provenance. The extractor reports the
// passage it used and that report is taken at its word — so a misreported index
// prints a passage citation on text that does not support the fact.
//
// The check is deliberately lexical and deliberately weak. It cannot confirm
// that a passage asserts the relation, only that the passage mentions both
// things being related. That is enough to catch the failure that matters: a
// fact attributed to a passage which never mentions either endpoint.
//
// Folding follows the corpus profile, because the extractor is asked for the
// shortest unambiguous name and will write "Gorakhnath" where an IAST source
// reads "gorakhanātha". Comparing raw strings would reject correct provenance
// across a whole corpus.
type EvidenceChecker struct {
	ix             *lexical.Index
	stripStemVowel bool
}

// NewEvidenceChecker builds a checker that folds the way the corpus does.
func NewEvidenceChecker(mode config.DiacriticMode, stripStemVowel bool) *EvidenceChecker {
	return &EvidenceChecker{
		ix:             lexical.NewWithFold(mode),
		stripStemVowel: stripStemVowel,
	}
}

// Check reports how strongly passage supports the relation subject-object.
func (e *EvidenceChecker) Check(passage, subject, object string) Evidence {
	if strings.TrimSpace(passage) == "" {
		return EvidenceNone
	}
	present := e.tokenSet(passage)
	if len(present) == 0 {
		return EvidenceNone
	}

	n := 0
	if e.mentions(present, subject) {
		n++
	}
	if e.mentions(present, object) {
		n++
	}
	switch n {
	case 2:
		return EvidenceBoth
	case 1:
		return EvidencePartial
	default:
		return EvidenceNone
	}
}

// mentions reports whether every significant token of name appears in the
// passage. Requiring all of them keeps a two-word entity from matching on its
// commonest half — "Nath Sampradaya" must not be evidenced by "nath" alone.
func (e *EvidenceChecker) mentions(present map[string]bool, name string) bool {
	toks := e.significant(name)
	if len(toks) == 0 {
		return false
	}
	for _, t := range toks {
		if !present[t] {
			return false
		}
	}
	return true
}

func (e *EvidenceChecker) tokenSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, t := range e.significant(text) {
		out[t] = true
	}
	return out
}

// significant tokenises the way the lexical index does, then applies the
// corpus's stem-vowel convention so "gorakhanatha" and "gorakhanath" compare
// equal where the profile says that is the same word.
func (e *EvidenceChecker) significant(text string) []string {
	toks := e.ix.Tokenize(text)
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if len([]rune(t)) < minEvidenceToken {
			continue
		}
		out = append(out, e.stem(t))
	}
	return out
}

func (e *EvidenceChecker) stem(t string) string {
	if !e.stripStemVowel {
		return t
	}
	if len(t) > 5 && strings.HasSuffix(t, "a") && !strings.HasSuffix(t, "aa") {
		return t[:len(t)-1]
	}
	return t
}
