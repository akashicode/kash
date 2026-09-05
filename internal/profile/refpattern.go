package profile

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/lexical"
)

// Reference-pattern detection.
//
// Measured against a real 61-document corpus, heading COVERAGE turns out to be
// a weak signal: genuine verse numbering covers only 2-32% of a document's
// headings, because most headings are not verse headings. What separates real
// numbering from noise is MONOTONICITY — a numbering scheme counts up, while
// page numbers, years and quantities do not:
//
//	Gyanarnava Tantra          coverage 0.28   monotonicity 0.96   accept
//	Rasa Hridaya Tantra        coverage 0.12   monotonicity 0.90   accept
//	Siddha Siddhanta Paddhati  coverage 0.32   monotonicity 0.88   accept
//	Anant ki Aur               coverage 0.24   monotonicity 0.03   reject
//	Svacchanda Tantra I        coverage 0.03   monotonicity 0.11   reject
//
// The rejects are prose commentaries with no verse numbering at all, so
// rejecting them is the correct answer rather than a missed detection.
//
// A scheme is judged on its own evidence, not on how many documents share it.
// Requiring two was safe on the 61-document corpus this was calibrated against,
// where any real scheme appeared in several, and wrong on a small mixed-genre
// one, where each work brings its own convention: a scripture numbering 45
// distinct passages as "97)" was discarded because the two commentaries beside
// it did not use that form. Document spread still counts — it is a term in the
// score below — but it no longer vetoes.

const (
	// minLabelHits is how often a label must appear corpus-wide before it is
	// treated as a numbering scheme rather than a coincidence.
	minLabelHits = 20
	// minDistinctValues stops a scheme being inferred from a handful of numbers.
	minDistinctValues = 10
	// acceptScore is the combined score a candidate must reach.
	acceptScore = 0.55
	// maxDetectedPatterns caps how many patterns are emitted. Every pattern
	// costs a regex pass over every heading at build time and every query at
	// serve time, so this is a real budget rather than a formality. Six is
	// enough for a corpus that numbers by several schemes at once: the corpus
	// this was calibrated against uses dhāraṇā, śloka, verse and bare "32)"
	// numbering in different editions of the same work.
	maxDetectedPatterns = 6
)

// labelRe finds "<word> <number>" in a heading: "Verse 25", "Clause 7(b)",
// "Dhāraṇā 49". The label is captured so it can be tallied and, once chosen,
// quoted into a generated pattern.
//
// The boundaries are spelled out rather than using \b. Go's RE2 defines \b over
// ASCII word characters only, so on transliterated text it fires *inside* a
// word: "ślokaḥ 5" was mined as the label "lokaḥ", splitting one real numbering
// scheme across several truncated spellings and inflating their hit counts.
var labelRe = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(\p{L}{3,15})\s*[-–—.]?\s*(\d[\d.]*)`)

// parenHeadingRe finds bare "32)" numbering used by some editions.
var parenHeadingRe = regexp.MustCompile(`^\s*(\d{1,4})\)`)

// ParenLabel and ParenPattern name the bare "48)" numbering scheme.
//
// Every other scheme is named by the word next to its number, so detection can
// call it what the corpus calls it. This one has no word — the number and a
// bracket are the whole marker — so detection has nothing to name it with and
// falls back to the generic section key. It is therefore the one scheme that
// depends on the model to name it, and the label is a protocol token between
// detection and that request rather than anything a reader sees.
const (
	ParenLabel   = "paren"
	ParenPattern = `^\s*(\d{1,4})\)`
)

// RefCandidate is a proposed numbering scheme with the evidence for it.
type RefCandidate struct {
	Label      string
	Pattern    string
	MetaKey    string
	Hits       int
	DocSpread  float64
	Coverage   float64
	Sequence   float64
	Score      float64
	Examples   []string
	sampleNums []int
}

// DetectRefPatterns proposes numbering patterns for a corpus.
//
// It only ever emits patterns built from literal labels it found and quoted
// with regexp.QuoteMeta. No pattern text originates outside this function —
// generated configuration runs against every query, so a document must not be
// able to inject one.
func DetectRefPatterns(docs []Doc) ([]RefCandidate, string) {
	type labelStat struct {
		hits     int
		docs     map[string]bool
		nums     map[string][]int // per document, in order
		examples []string
	}
	stats := map[string]*labelStat{}

	totalHeadings := 0
	parenStat := &labelStat{docs: map[string]bool{}, nums: map[string][]int{}}

	for _, d := range docs {
		headings := chunker.DocumentHeadings(d.Content)
		totalHeadings += len(headings)

		for _, h := range headings {
			if m := parenHeadingRe.FindStringSubmatch(h); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					parenStat.hits++
					parenStat.docs[d.Name] = true
					parenStat.nums[d.Name] = append(parenStat.nums[d.Name], n)
					if len(parenStat.examples) < 5 {
						parenStat.examples = append(parenStat.examples, h)
					}
				}
			}

			for _, m := range labelRe.FindAllStringSubmatch(h, -1) {
				label := strings.ToLower(m[1])
				first := strings.SplitN(m[2], ".", 2)[0]
				n, err := strconv.Atoi(first)
				if err != nil {
					continue
				}
				st := stats[label]
				if st == nil {
					st = &labelStat{docs: map[string]bool{}, nums: map[string][]int{}}
					stats[label] = st
				}
				st.hits++
				st.docs[d.Name] = true
				st.nums[d.Name] = append(st.nums[d.Name], n)
				if len(st.examples) < 5 {
					st.examples = append(st.examples, h)
				}
			}
		}
	}

	var cands []RefCandidate
	consider := func(label string, st *labelStat, pattern, metaKey string) {
		if st.hits < minLabelHits {
			return
		}
		distinct := map[int]bool{}
		for _, ns := range st.nums {
			for _, n := range ns {
				distinct[n] = true
			}
		}
		if len(distinct) < minDistinctValues {
			return
		}

		seq := bestSequenceScore(st.nums)
		coverage := 0.0
		if totalHeadings > 0 {
			coverage = float64(st.hits) / float64(totalHeadings)
		}
		spread := float64(len(st.docs)) / float64(max(len(docs), 1))
		if len(docs) == 1 {
			spread = 1
		}

		// Weighted from measurement, not intuition: genuine numbering covers only
		// 2-32% of headings, so coverage cannot carry the decision. Monotonic
		// sequence is what separates a numbering scheme from page numbers.
		score := 0.60*seq + 0.15*coverage + 0.15*spread + 0.10*yearPenaltyFree(distinct)
		cands = append(cands, RefCandidate{
			Label: label, Pattern: pattern, MetaKey: metaKey,
			Hits: st.hits, DocSpread: spread, Coverage: coverage,
			Sequence: seq, Score: score, Examples: st.examples,
		})
	}

	for label, st := range stats {
		if isStopLabel(label) {
			continue
		}
		// Same non-ASCII-safe boundary as labelRe, for the same reason.
		pattern := `(?i)(?:^|[^\p{L}])` + regexp.QuoteMeta(label) + `s?\s*[-–—.]?\s*(\d[\d.]*)`
		consider(label, st, pattern, sanitizeMetaKey(label))
	}
	consider(ParenLabel, parenStat, ParenPattern, chunker.MetaSection)

	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].Label < cands[j].Label
	})

	var accepted []RefCandidate
	seenKey := map[string]bool{}
	for _, c := range cands {
		if c.Score < acceptScore || seenKey[c.MetaKey] {
			continue
		}
		if !validPattern(c.Pattern) {
			continue
		}
		seenKey[c.MetaKey] = true
		accepted = append(accepted, c)
		if len(accepted) == maxDetectedPatterns {
			break
		}
	}

	if len(accepted) == 0 {
		return nil, fmt.Sprintf("no numbering scheme found in %d headings across %d documents", totalHeadings, len(docs))
	}

	var parts []string
	for _, c := range accepted {
		parts = append(parts, fmt.Sprintf("%q (%d hits, sequence %.2f, coverage %.2f)",
			c.Label, c.Hits, c.Sequence, c.Coverage))
	}
	return accepted, fmt.Sprintf("across %d headings in %d documents: %s",
		totalHeadings, len(docs), strings.Join(parts, "; "))
}

// bestSequenceScore takes the strongest per-document sequence score. Scoring
// per document matters: numbering restarts at each volume, so pooling the
// numbers from sixty documents would look like noise even for a perfect scheme.
//
// The best document decides, not the average. A scheme is a property of the
// work that uses it, and one work quoting another's numbering in passing is not
// evidence against that numbering. Averaging let the passing mention outvote
// the real one: a scripture numbering 101 of its own verses scored 0.84, a
// commentary citing nine of them scored 0.49, and the mean of 0.66 described
// neither.
func bestSequenceScore(perDoc map[string][]int) float64 {
	best := 0.0
	for _, nums := range perDoc {
		if len(nums) < 3 {
			continue
		}
		if s := sequenceScore(nums); s > best {
			best = s
		}
	}
	return best
}

// sequenceScore blends how consistently values ascend, how densely they cover
// their own range, and whether they start near 1.
func sequenceScore(nums []int) float64 {
	if len(nums) < 2 {
		return 0
	}

	ascending := 0
	for i := 1; i < len(nums); i++ {
		if nums[i] > nums[i-1] {
			ascending++
		}
	}
	monotonic := float64(ascending) / float64(len(nums)-1)

	distinct := map[int]bool{}
	maxVal, minVal := nums[0], nums[0]
	for _, n := range nums {
		distinct[n] = true
		if n > maxVal {
			maxVal = n
		}
		if n < minVal {
			minVal = n
		}
	}
	density := 0.0
	if maxVal > 0 {
		density = float64(len(distinct)) / float64(maxVal)
		if density > 1 {
			density = 1
		}
	}

	// Numbering that begins near the start of its own range, rather than at an
	// arbitrary high number the way page numbers and quantities do.
	//
	// Requiring the first value to be 1, 2 or 3 assumed every work is quoted
	// whole. An anthology quoting ślokas 7 to 102 begins at the beginning of
	// what it quotes, and was scored as though it began nowhere — enough on its
	// own to sink a scheme carrying 41 headings and 40 distinct numbers. The
	// test is now relative to the range, so a run starting a short way in still
	// counts while one starting at 200 of 400 does not.
	startsLow := 0.0
	if minVal <= 3 || (maxVal > 0 && minVal <= maxVal/10) {
		startsLow = 1
	}

	return 0.5*monotonic + 0.3*density + 0.2*startsLow
}

// yearPenaltyFree returns 1 when the captured values do not look like years,
// 0 when most of them do. Years ascend and would otherwise score well.
func yearPenaltyFree(distinct map[int]bool) float64 {
	if len(distinct) == 0 {
		return 0
	}
	yearish := 0
	for n := range distinct {
		if n >= 1500 && n <= 2100 {
			yearish++
		}
	}
	if float64(yearish)/float64(len(distinct)) > 0.5 {
		return 0
	}
	return 1
}

// stopLabels are words that commonly precede a number without naming a
// numbering scheme.
var stopLabels = map[string]bool{
	"page": true, "line": true, "note": true, "figure": true, "table": true,
	"vol": true, "volume": true, "issue": true, "isbn": true, "issn": true,
	"the": true, "and": true, "for": true, "from": true, "with": true,
	"jan": true, "feb": true, "mar": true, "apr": true, "jun": true,
	"jul": true, "aug": true, "sep": true, "oct": true, "nov": true, "dec": true,
}

func isStopLabel(label string) bool { return stopLabels[label] }

// metaKeyRe is the shape a metadata key must have: it becomes a chunk metadata
// field and a query-time lookup key.
var metaKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,20}$`)

// reservedMetaKeys must never be produced by detection — writing to one would
// corrupt the chunk's own structural metadata.
var reservedMetaKeys = map[string]bool{
	chunker.MetaBook: true, chunker.MetaHeading: true, chunker.MetaBreadcrumb: true,
	chunker.MetaContentType: true, chunker.MetaNoiseScore: true,
}

// sanitizeMetaKey turns a mined label into a usable metadata key.
//
// Transliterated labels are folded to ASCII first, so "dhāraṇā" becomes the key
// "dharana" — the name a reader would actually type. Substituting characters
// instead would mangle it into "dh_ra", which is worse than a generic name
// because it looks deliberate. Reusing the same fold table the lexical index
// uses keeps keys and query tokens spelled consistently. Both alphabets are
// folded here regardless of the corpus mode, because a key is a name rather
// than a search token and must be typeable.
func sanitizeMetaKey(label string) string {
	folded := lexical.NewWithFold(config.DiacriticBoth).Fold(label)
	key := strings.TrimSuffix(strings.ToLower(folded), "s")
	if !isASCIILower(key) || !metaKeyRe.MatchString(key) || reservedMetaKeys[key] {
		return chunker.MetaSection
	}
	return key
}

// isASCIILower reports whether a label is plain ASCII letters, the only case
// where it can be used as a metadata key verbatim.
func isASCIILower(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// validPattern applies the same gate the chunker uses, so detection can never
// emit a pattern the compiler would reject or that would panic in use.
func validPattern(pattern string) bool {
	if len(pattern) > chunker.MaxRefPatternLen {
		return false
	}
	re, err := regexp.Compile(pattern)
	return err == nil && re.NumSubexp() == 1
}

// ToRefPatterns converts accepted candidates into config entries.
func ToRefPatterns(cands []RefCandidate) []config.RefPattern {
	out := make([]config.RefPattern, 0, len(cands))
	for _, c := range cands {
		out = append(out, config.RefPattern{Pattern: c.Pattern, MetaKey: c.MetaKey})
	}
	return out
}

