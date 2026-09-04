package profile

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/config"
)

// Doc is the minimal view of a source document the detectors need. Declared
// here rather than importing reader so the detectors stay pure functions that
// tests can drive with literals.
type Doc struct {
	Name    string
	Content string
}

// --- Diacritic mode -------------------------------------------------------

// The discriminator sets must be DISJOINT. "ñ" appears in both the Latin and
// IAST fold tables (internal/lexical/bm25.go), so counting it toward either
// would make a Spanish corpus read as Sanskrit. It is deliberately in neither.
var (
	iastOnlyMarks  = []rune("āīūṛṝḷḹṅṇṃṁṉṭḍśṣḥḻ")
	latinOnlyMarks = []rune("áàâäãåéèêëíìîïóòôöõøúùûüçýÿšžðþ")
)

// Detection floors. Measured against a real 61-document corpus: IAST documents
// score 16-18% marks per letter while every non-IAST document scores exactly
// 0.00%, so any threshold in this range separates them. The absolute floor
// stops a handful of stray characters in an otherwise ASCII corpus from
// flipping the mode.
const (
	minMarkOccurrences = 200
	minMarkRate        = 0.0002
)

// DetectDiacritics chooses the fold mode from the characters the corpus
// actually contains.
//
// This is measurable exactly, so it is never asked of a model.
func DetectDiacritics(docs []Doc) (config.DiacriticMode, string) {
	iastSet := runeSet(iastOnlyMarks)
	latinSet := runeSet(latinOnlyMarks)

	var iastHits, latinHits, letters int
	iastDocs, latinDocs := 0, 0

	for _, d := range docs {
		di, dl := 0, 0
		for _, r := range strings.ToLower(d.Content) {
			switch {
			case iastSet[r]:
				di++
			case latinSet[r]:
				dl++
			}
			if unicode.IsLetter(r) {
				letters++
			}
		}
		iastHits += di
		latinHits += dl
		if di > 0 {
			iastDocs++
		}
		if dl > 0 {
			latinDocs++
		}
	}

	minDocs := 1
	if len(docs) >= 4 {
		minDocs = 2
	}

	present := func(hits, inDocs int) bool {
		if letters == 0 {
			return false
		}
		return hits >= minMarkOccurrences &&
			float64(hits)/float64(letters) >= minMarkRate &&
			inDocs >= minDocs
	}

	hasIAST := present(iastHits, iastDocs)
	hasLatin := present(latinHits, latinDocs)

	evidence := fmt.Sprintf("IAST marks: %d in %d/%d docs; Latin marks: %d in %d/%d docs (%d letters scanned)",
		iastHits, iastDocs, len(docs), latinHits, latinDocs, len(docs), letters)

	switch {
	case hasIAST && hasLatin:
		return config.DiacriticBoth, evidence
	case hasIAST:
		return config.DiacriticIAST, evidence
	case hasLatin:
		return config.DiacriticLatin, evidence
	default:
		return config.DiacriticNone, evidence
	}
}

// --- Stem vowel -----------------------------------------------------------

// Stem-vowel decision bands. Below the low band the answer is a confident no;
// above the high band a confident yes; between them there is real ambiguity
// and the caller may ask the model.
const (
	stemVowelPairsHigh = 25
	stemVowelRateHigh  = 0.005
	stemVowelPairsLow  = 8
	// stemVowelPairsDecisive is enough evidence on its own, whatever the rate.
	stemVowelPairsDecisive = 200
)

// StemVowelEvidence reports what the corpus says about folding a trailing stem
// vowel, including the undecided case.
type StemVowelEvidence struct {
	Pairs     int
	Rate      float64
	Examples  []string
	Decided   bool
	Value     bool
	Narrative string
}

// DetectStemVowel measures whether stem-vowel variants of the same token
// actually co-occur in this corpus.
//
// The useful question is not "is this Sanskrit?" — which a model would have to
// guess at — but the one the flag actually controls: do "gorakhnath" and
// "gorakhnatha" both appear? That is directly countable.
func DetectStemVowel(docs []Doc) StemVowelEvidence {
	counts := map[string]int{}
	for _, d := range docs {
		for _, tok := range strings.FieldsFunc(d.Content, func(r rune) bool {
			return !unicode.IsLetter(r)
		}) {
			if len([]rune(tok)) >= 5 {
				counts[strings.ToLower(tok)]++
			}
		}
	}

	const minOccurrences = 3
	var pairs int
	var examples []string
	seen := map[string]bool{}

	for tok, n := range counts {
		if n < minOccurrences {
			continue
		}
		// stripFinalVowel in fusion.go also folds a trailing m/h, so the same
		// endings are tested here.
		for _, v := range []string{"a", "i", "u", "o", "e", "m", "h"} {
			base := tok + v
			if counts[base] < minOccurrences || seen[tok+"|"+base] {
				continue
			}
			seen[tok+"|"+base] = true
			pairs++
			if len(examples) < 10 {
				examples = append(examples, tok+"/"+base)
			}
		}
	}

	var rate float64
	if len(counts) > 0 {
		rate = float64(pairs) / float64(len(counts))
	}
	sort.Strings(examples)

	ev := StemVowelEvidence{Pairs: pairs, Rate: rate, Examples: examples}
	switch {
	// The rate guards a small corpus where a few pairs could be coincidence. It
	// must not veto overwhelming absolute evidence: a 23M-letter corpus dilutes
	// the rate against every distinct token, so 1500+ pairs reads as 0.003.
	case pairs >= stemVowelPairsDecisive || (pairs >= stemVowelPairsHigh && rate >= stemVowelRateHigh):
		ev.Decided, ev.Value = true, true
		ev.Narrative = fmt.Sprintf("%d stem-vowel variant pairs (%.3f of candidate tokens), e.g. %s",
			pairs, rate, strings.Join(examples, ", "))
	case pairs < stemVowelPairsLow:
		ev.Decided, ev.Value = true, false
		ev.Narrative = fmt.Sprintf("only %d stem-vowel variant pairs — folding would merge unrelated words", pairs)
	default:
		ev.Narrative = fmt.Sprintf("%d stem-vowel variant pairs (%.3f) is inconclusive, e.g. %s",
			pairs, rate, strings.Join(examples, ", "))
	}
	return ev
}

// --- Title stopwords ------------------------------------------------------

// minDocsForTitleStats gates title-frequency analysis. Below it, document
// frequency is noise: with three documents one shared word is already 33%.
const minDocsForTitleStats = 8

// titleStopwordDF is the share of titles a token must appear in before it is
// treated as carrying no discriminating power.
// Measured against a real corpus: the most common genre word ("tantra")
// appears in 30% of titles, so a 0.4 threshold finds nothing at all. At 0.15
// the tokens found are genre words and recurring editor names — exactly the
// ones that caused false work merges.
const titleStopwordDF = 0.15

// DetectTitleStopwords finds words that appear in so many document titles that
// they cannot distinguish one work from another.
//
// Tokenization goes through chunker.BookTitle so it matches exactly what work
// grouping sees; deriving stopwords for tokens the grouper never encounters
// would be worse than useless.
func DetectTitleStopwords(docs []Doc, defaults []string) ([]string, string) {
	if len(docs) < minDocsForTitleStats {
		return nil, fmt.Sprintf("corpus too small (%d docs) for title-frequency analysis; keeping generic stopwords", len(docs))
	}

	df := map[string]int{}
	for _, d := range docs {
		seen := map[string]bool{}
		for _, tok := range strings.Fields(strings.ToLower(chunker.BookTitle(d.Name))) {
			tok = strings.Trim(tok, ".,:;()[]")
			if len([]rune(tok)) < 4 || seen[tok] {
				continue
			}
			seen[tok] = true
			df[tok]++
		}
	}

	have := map[string]bool{}
	for _, w := range defaults {
		have[strings.ToLower(w)] = true
	}

	threshold := int(float64(len(docs)) * titleStopwordDF)
	if threshold < 2 {
		threshold = 2
	}

	var added []string
	for tok, n := range df {
		if n >= threshold && !have[tok] {
			added = append(added, tok)
		}
	}
	sort.Strings(added)

	const maxAdded = 40
	if len(added) > maxAdded {
		added = added[:maxAdded]
	}
	if len(added) == 0 {
		return nil, fmt.Sprintf("no title token appears in %d+ of %d titles", threshold, len(docs))
	}

	out := append(append([]string{}, defaults...), added...)
	return out, fmt.Sprintf("%d token(s) appear in %d+ of %d titles and cannot distinguish works: %s",
		len(added), threshold, len(docs), strings.Join(added, ", "))
}

func runeSet(rs []rune) map[rune]bool {
	m := make(map[rune]bool, len(rs))
	for _, r := range rs {
		m[r] = true
	}
	return m
}
