package server

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/lexical"
	"github.com/akashicode/kash/internal/vector"
)

// rrfK is the Reciprocal Rank Fusion constant. 60 is the value from the
// original RRF paper and the one zvec-grep uses at both of its fusion levels.
// Fusion is unweighted: with no evaluation set to tune against, a hand-picked
// lexical/semantic ratio is a guess that looks like a decision.
const rrfK = 60.0

// noisePenalty is how much a chunk's apparatus score suppresses it. Index
// tables, concordances and page listings are term-dense, so they score
// moderately against almost any query — in the corpus that motivated this, a
// query for "dharana 49" returned five index tables and no scripture. They are
// down-ranked rather than dropped: a concordance is legitimate content for a
// lookup, it just must not outrank an actual explanation.
const noisePenalty = 0.7

// exactRefBoost is added to a chunk whose metadata exactly matches a reference
// named in the query. An exact verse match is the strongest signal available
// and should outrank any similarity score.
const exactRefBoost = 1.0

// candidate accumulates evidence for one chunk across retrieval routes.
type candidate struct {
	id     string
	result vector.SearchResult
	score  float64
	routes []string
}

// fuseRankLists merges ranked ID lists by Reciprocal Rank Fusion.
//
// RRF combines rankings without needing scores to be comparable, which matters
// here because cosine similarity and BM25 live on different scales and a
// weighted sum of the two is meaningless without calibration.
func fuseRankLists(lists map[string][]string) map[string]*candidate {
	out := map[string]*candidate{}
	for route, ids := range lists {
		for rank, id := range ids {
			c, ok := out[id]
			if !ok {
				c = &candidate{id: id}
				out[id] = c
			}
			c.score += 1 / (rrfK + float64(rank))
			c.routes = append(c.routes, route)
		}
	}
	return out
}

// applyNoisePenalty scales a candidate's score down by its apparatus score.
func applyNoisePenalty(c *candidate) {
	noise, err := strconv.ParseFloat(c.result.Metadata[chunker.MetaNoiseScore], 64)
	if err != nil || noise <= 0 {
		return
	}
	if noise > 1 {
		noise = 1
	}
	c.score *= 1 - noisePenalty*noise
}

// refPattern captures a structural reference named in a query: "dharana 49",
// "verse 32", "sutra 12". These are the queries dense retrieval cannot serve,
// because the number carries almost no signal in an embedding.
var refPattern = regexp.MustCompile(`(?i)\b(dharana|dhāraṇā|dharan[aā]|vidhi|verse|sloka|śloka|shloka|sutra|sūtra)\s*[-–—.:]?\s*(\d{1,3})\b`)

// queryRefs extracts structural references from a query, returning the metadata
// fields and value to look up.
func queryRefs(query string) []queryRef {
	var refs []queryRef
	for _, m := range refPattern.FindAllStringSubmatch(query, -1) {
		word, num := strings.ToLower(m[1]), m[2]
		// A reader asking for "dharana 49" wants that technique whichever way
		// their edition numbers it: the Sanskrit editions head it "dhāraṇā-49",
		// the Osho/Hindi edition "vidhi 49", and the English editions number the
		// same 112 techniques as verses. Try every field.
		switch word {
		case "verse", "sloka", "śloka", "shloka", "sutra", "sūtra":
			refs = append(refs,
				queryRef{Field: chunker.MetaVerse, Value: num},
				queryRef{Field: chunker.MetaDharana, Value: num})
		default:
			refs = append(refs,
				queryRef{Field: chunker.MetaDharana, Value: num},
				queryRef{Field: chunker.MetaVerse, Value: num})
		}
	}
	return refs
}

// queryRef is a metadata field and value to match exactly.
type queryRef struct {
	Field string
	Value string
}

// maxExactHits caps the exact-reference route.
//
// A number alone is ambiguous across a multi-book corpus: every text has a
// verse 49. Without a cap the route lifted all of them above genuinely better
// matches, so a query for "Verse 32 Vigyan Bhairava" returned verse 32 from
// four unrelated books. Capping the route to its best few keeps the remaining
// slots for ordinary relevance.
const maxExactHits = 6

// lexicalRoutes builds the BM25 and exact-reference rank lists for a query.
//
// Exact hits are ordered by their BM25 score against the whole query, not by
// document order: that is what makes the rest of the query — the book name, the
// surrounding words — decide which text's verse 49 is meant.
func lexicalRoutes(ix *lexical.Index, query string, depth int) (lex, exact []string) {
	hits := ix.Search(query, depth)
	score := make(map[string]float64, len(hits))
	for _, h := range hits {
		lex = append(lex, h.ID)
		score[h.ID] = h.Score
	}

	type scored struct {
		id string
		s  float64
	}

	// Fields are tried in priority order and the first one that matches wins.
	//
	// "dharana 49" names a technique, so chunks whose dhāraṇā number is 49 are
	// what was asked for. Merging them with every book's *verse* 49 — sixty
	// texts, all with a verse 49 — buried the intended passage: measured over
	// all 112 techniques, mixing the fields gave recall@5 of 0.38; trying the
	// named field first gives 0.88.
	//
	// The verse field stays as a fallback for editions that number the same
	// techniques as verses. Treat that fallback as approximate: where an edition
	// numbers ślokas separately, the two do not line up — in the Dwivedi
	// Vijñānabhairava, dhāraṇā 49 is verse 72 — so a fallback hit may be a
	// neighbouring technique rather than the one asked for. It is used only when
	// no edition tags that dhāraṇā number at all, which is the case for 12 of
	// the 112 techniques in this corpus.
	seen := map[string]bool{}
	for _, ref := range queryRefs(query) {
		var ex []scored
		for _, r := range ix.FindByRef(ref.Field, ref.Value) {
			if seen[r.ID] {
				continue
			}
			seen[r.ID] = true
			ex = append(ex, scored{id: r.ID, s: score[r.ID]})
		}
		if len(ex) == 0 {
			continue
		}
		sort.Slice(ex, func(i, j int) bool {
			if ex[i].s != ex[j].s {
				return ex[i].s > ex[j].s
			}
			return ex[i].id < ex[j].id
		})
		if len(ex) > maxExactHits {
			ex = ex[:maxExactHits]
		}
		for _, e := range ex {
			exact = append(exact, e.id)
		}
		break
	}
	return lex, exact
}

// rankCandidates orders candidates by fused score, breaking ties on ID so the
// ordering is deterministic.
func rankCandidates(cands map[string]*candidate) []*candidate {
	out := make([]*candidate, 0, len(cands))
	for _, c := range cands {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].id < out[j].id
	})
	return out
}

// Diversity must be applied per work, not per file. The corpus that motivated
// this holds six editions of the Vijñāna Bhairava under six filenames; capping
// "per source" let those six compete as six independent books, so a question
// about one dhāraṇā returned near-identical passages from two editions while
// the edition that actually explains it never surfaced.
//
// Grouping them is not exact string matching: the same text appears as
// "vigyan-bhairava-tantra", "vigyan-bhairav-tantra-hindi" and
// "VijnanaBhairava-khemraj" — differing by transliteration scheme, final
// vowels, and whether words are separated at all. The rule used here is
// containment of the longest distinctive stemmed token, which also correctly
// groups the volumes of a multi-volume work.
type workGrouper struct {
	canonical map[string]string // raw title -> canonical work key
	tokens    []workToken       // every known token, mapped to its work
}

func newWorkGrouper() *workGrouper {
	return &workGrouper{canonical: map[string]string{}}
}

// Key returns a stable work key for a result, creating one on first sight.
func (g *workGrouper) Key(r vector.SearchResult) string {
	raw := r.Metadata[chunker.MetaBook]
	if raw == "" {
		raw = r.Source
	}
	if k, ok := g.canonical[raw]; ok {
		return k
	}

	toks := titleTokens(raw)
	if len(toks) == 0 {
		g.canonical[raw] = raw
		return raw
	}
	// Every token of a known work is matchable, not just the one that named it:
	// "vigyan-bhairava-tantra" registers both "vigyan" and "bhairav", which is
	// what lets "VijnanaBhairava-khemraj" find the family it belongs to.
	for _, known := range g.tokens {
		for _, tok := range toks {
			if relatedToken(tok, known.token) {
				g.canonical[raw] = known.work
				return known.work
			}
		}
	}
	work := toks[0]
	for _, tok := range toks {
		g.tokens = append(g.tokens, workToken{token: tok, work: work})
	}
	g.canonical[raw] = work
	return work
}

// workToken maps one title token to the work it identifies. It is a slice
// rather than a map so grouping is deterministic across runs.
type workToken struct {
	token string
	work  string
}

// minContainmentLen is the shortest token allowed to match by containment.
// Below it, containment produces false merges: "rasa" is a substring of
// "rasarnava", but Rasa Hṛdaya Tantra and the Rasārṇavam are different works.
const minContainmentLen = 6

// relatedToken reports whether two title tokens identify the same work.
func relatedToken(a, b string) bool {
	if a == b {
		return true
	}
	shorter, longer := a, b
	if len(longer) < len(shorter) {
		shorter, longer = longer, shorter
	}
	return len(shorter) >= minContainmentLen && strings.Contains(longer, shorter)
}

var nonWordRe = regexp.MustCompile(`[^a-z0-9]+`)

// titleStopwords are words that identify an edition or a format rather than the
// work: translators, publishers, volume markers, file-pipeline suffixes.
var titleStopwords = map[string]bool{
	"the": true, "of": true, "and": true, "with": true, "by": true, "for": true,
	"vol": true, "volume": true, "part": true, "final": true, "iast": true,
	"ocr": true, "original": true, "hindi": true, "english": true,
	"sanskrit": true, "translation": true, "commentary": true, "tika": true,
	"tantra": true, "tantram": true, "tantras": true,
	// Generic Sanskrit title words: a shared "paddhati" (manual) merged
	// Gorakṣa Paddhati with Siddha Siddhānta Paddhati, which are distinct texts.
	"paddhati": true, "paddhat": true, "samhita": true, "samhit": true,
	"shastra": true, "sastra": true, "grantha": true, "granth": true,
	"yoga": true, "yog": true, "prasang": true, "katha": true, "kath": true,
	// Honorifics and forms of address, which name a person rather than a work.
	"swami": true, "shri": true, "sri": true, "maharaj": true, "kaviraj": true,
	"pandit": true, "acharya": true, "acary": true,
}

var creditSplitRe = regexp.MustCompile(`(?i)\s+(?:-|–|—|by|with)\s+`)

// titleTokens reduces a title to its identifying tokens, most significant first.
//
// The translator or publisher credit is dropped first: these titles follow
// "Work - Translator" or "Work by Author", and keeping the credit merged two
// unrelated Gopinath Kaviraj works into one "work" purely because they share an
// editor. What remains is folded, stemmed and stripped of edition words.
func titleTokens(title string) []string {
	head := creditSplitRe.Split(title, 2)[0]
	s := foldDiacritics(strings.ToLower(head))
	s = nonWordRe.ReplaceAllString(s, " ")

	var out []string
	for _, f := range strings.Fields(s) {
		if titleStopwords[f] || len([]rune(f)) < 4 {
			continue
		}
		if _, err := strconv.Atoi(f); err == nil {
			continue
		}
		out = append(out, stripFinalVowel(f))
	}
	return out
}

// stripFinalVowel normalizes the Sanskrit/Hindi difference between "bhairava"
// and "bhairav", which is purely a transliteration convention.
func stripFinalVowel(s string) string {
	if len(s) < 5 {
		return s
	}
	switch s[len(s)-1] {
	case 'a', 'i', 'u', 'o', 'e', 'm', 'h':
		return s[:len(s)-1]
	}
	return s
}

// iastFolds maps the IAST diacritics used across this corpus to ASCII.
var iastFolds = strings.NewReplacer(
	"ā", "a", "ī", "i", "ū", "u", "ṛ", "r", "ṝ", "r", "ḷ", "l",
	"ṅ", "n", "ñ", "n", "ṇ", "n", "ṃ", "m", "ṁ", "m",
	"ṭ", "t", "ḍ", "d", "ś", "s", "ṣ", "s", "ḥ", "h",
	"é", "e", "è", "e", "ê", "e", "ô", "o",
)

func foldDiacritics(s string) string { return iastFolds.Replace(s) }

// selectDiverse fills topK slots, capping how many may come from one work while
// letting a dominant work take the whole slate when the evidence really is
// concentrated there.
//
// The previous rule capped every source at (topK+1)/2 unconditionally, which
// for a focused question about a single text — the normal case — discarded the
// best-ranked chunks in favour of lower-ranked ones from other books.
func selectDiverse(ranked []*candidate, topK int) []*candidate {
	if len(ranked) <= topK {
		return ranked
	}

	g := newWorkGrouper()

	maxPerWork := (topK + 1) / 2
	if maxPerWork < 1 {
		maxPerWork = 1
	}

	// When the top results overwhelmingly agree on one work, the question is
	// about that work; spreading the slate across other books would answer a
	// question nobody asked.
	if dominantWork(g, ranked, topK) {
		maxPerWork = topK
	}

	var (
		selected []*candidate
		skipped  []*candidate
		perWork  = map[string]int{}
	)
	for _, c := range ranked {
		if len(selected) >= topK {
			break
		}
		key := g.Key(c.result)
		if perWork[key] >= maxPerWork {
			skipped = append(skipped, c)
			continue
		}
		perWork[key]++
		selected = append(selected, c)
	}
	for _, c := range skipped {
		if len(selected) >= topK {
			break
		}
		selected = append(selected, c)
	}
	return selected
}

// dominantWork reports whether one work holds most of the top candidates.
func dominantWork(g *workGrouper, ranked []*candidate, topK int) bool {
	window := topK * 2
	if window > len(ranked) {
		window = len(ranked)
	}
	if window == 0 {
		return false
	}
	counts := map[string]int{}
	best := 0
	for _, c := range ranked[:window] {
		k := g.Key(c.result)
		counts[k]++
		if counts[k] > best {
			best = counts[k]
		}
	}
	return float64(best)/float64(window) >= 0.6
}

// dedupeNearDuplicates drops candidates whose text substantially repeats a
// higher-ranked one. Consecutive chunks share an overlap tail by construction,
// so without this a small result slate can spend two of its slots on the same
// passage.
func dedupeNearDuplicates(ranked []*candidate, threshold float64) []*candidate {
	var kept []*candidate
	var sigs []map[string]bool

	for _, c := range ranked {
		sig := shingles(c.result.Content)
		dup := false
		for _, prev := range sigs {
			if jaccard(sig, prev) >= threshold {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		kept = append(kept, c)
		sigs = append(sigs, sig)
	}
	return kept
}

// shingles builds a set of overlapping word trigrams for similarity comparison.
func shingles(text string) map[string]bool {
	words := strings.Fields(strings.ToLower(text))
	out := map[string]bool{}
	for i := 0; i+2 < len(words); i++ {
		out[words[i]+" "+words[i+1]+" "+words[i+2]] = true
	}
	return out
}

// jaccard returns the overlap of two shingle sets, measured against the smaller
// one so a short chunk fully contained in a longer one counts as duplicate.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for s := range a {
		if b[s] {
			inter++
		}
	}
	smaller := len(a)
	if len(b) < smaller {
		smaller = len(b)
	}
	return float64(inter) / float64(smaller)
}
