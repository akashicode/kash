package server

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/akashicode/kash/internal/chunker"
	agentconfig "github.com/akashicode/kash/internal/config"
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
// moderately against almost any query and crowd genuine prose out of a small
// result slate. They are down-ranked rather than dropped: a concordance is
// legitimate content for a lookup, it just must not outrank an actual
// explanation.
const noisePenalty = 0.7

// exactRefBoost is added to a chunk whose metadata exactly matches a reference
// named in the query. An exact match is the strongest signal available and
// should outrank any similarity score.
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

// queryRef is a metadata field and value to match exactly.
type queryRef struct {
	Field string
	Value string
}

// refRouter holds compiled corpus-specific reference patterns. It is built
// once at server startup from the domain config and reused for every query.
type refRouter struct {
	matchers []routerMatcher
}

// routerMatcher pairs a compiled regexp (one capture group = the number) with
// the metadata key to look up in FindByRef.
type routerMatcher struct {
	re      *regexp.Regexp
	metaKey string
}

// buildRefRouter compiles the RefPatterns from the domain config into a
// refRouter. Patterns that fail to compile are skipped (not fatal).
//
// Compilation goes through chunker.CompileRefPattern, the same helper the
// chunker uses to tag chunks. That shared call is the point: a query is matched
// against the patterns that tagged the corpus, so if the two sides compiled
// them differently, a query could name a reference the index never recorded.
func buildRefRouter(patterns []agentconfig.RefPattern) *refRouter {
	r := &refRouter{}
	for _, p := range patterns {
		re, err := chunker.CompileRefPattern(p)
		if err != nil {
			continue
		}
		r.matchers = append(r.matchers, routerMatcher{re: re, metaKey: p.MetaKey})
	}
	return r
}

// queryRefs extracts structural references from a query using the compiled
// patterns. Returns field+value pairs to look up via FindByRef.
func (r *refRouter) queryRefs(query string) []queryRef {
	if r == nil {
		return nil
	}
	var refs []queryRef
	seen := map[string]bool{}
	for _, m := range r.matchers {
		for _, hit := range m.re.FindAllStringSubmatch(query, -1) {
			key := m.metaKey + ":" + hit[1]
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, queryRef{Field: m.metaKey, Value: hit[1]})
		}
	}
	return refs
}

// maxExactHits caps the exact-reference route.
//
// A number alone is ambiguous across a multi-document corpus: every document
// may have a "Section 4". Without a cap the route lifts all of them above
// genuinely better matches. Capping the route to its best few keeps the
// remaining slots for ordinary relevance.
const maxExactHits = 6

// lexicalRoutes builds the BM25 and exact-reference rank lists for a query.
//
// Exact hits are ordered by their BM25 score against the whole query, not by
// document order: that is what makes the rest of the query — the document
// name, the surrounding words — decide which document's section is meant.
func lexicalRoutes(ix *lexical.Index, router *refRouter, query string, depth int) (lex, exact []string) {
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

	seen := map[string]bool{}
	for _, ref := range router.queryRefs(query) {
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
	stopwords map[string]bool   // domain-specific title stopwords
	foldFn    func(string) string
	stripStem bool
}

func newWorkGrouper(stopwords map[string]bool, foldFn func(string) string, stripStem bool) *workGrouper {
	return &workGrouper{
		canonical: map[string]string{},
		stopwords: stopwords,
		foldFn:    foldFn,
		stripStem: stripStem,
	}
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

	toks := g.titleTokens(raw)
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
// Below it, containment produces false merges.
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
var creditSplitRe = regexp.MustCompile(`(?i)\s+(?:-|–|—|by|with)\s+`)

// titleTokens reduces a title to its identifying tokens, most significant first.
//
// The translator or publisher credit is dropped first: these titles follow
// "Work - Translator" or "Work by Author". What remains is folded, stemmed
// and stripped of edition words from the domain config's stopwords list.
func (g *workGrouper) titleTokens(title string) []string {
	head := creditSplitRe.Split(title, 2)[0]
	s := g.foldFn(strings.ToLower(head))
	s = nonWordRe.ReplaceAllString(s, " ")

	var out []string
	for _, f := range strings.Fields(s) {
		if g.stopwords[f] || len([]rune(f)) < 4 {
			continue
		}
		if _, err := strconv.Atoi(f); err == nil {
			continue
		}
		if g.stripStem {
			f = stripFinalVowel(f)
		}
		out = append(out, f)
	}
	return out
}

// stripFinalVowel normalises the Sanskrit/Hindi difference between "bhairava"
// and "bhairav". Only used when ChunkerConfig.StripTitleStemVowel is true.
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

// fusionConfig holds all corpus-specific fusion settings compiled from the
// domain config. Build one at server startup and pass it to the fusion funcs.
type fusionConfig struct {
	router    *refRouter
	grouper   func() *workGrouper // factory so each query gets a fresh grouper
	foldTitle func(string) string
}

// buildFusionConfig compiles a fusionConfig from the domain config.
func buildFusionConfig(dc agentconfig.DomainConfig) *fusionConfig {
	router := buildRefRouter(dc.Chunker.RefPatterns)

	// Build the title stopwords set from config.
	stops := make(map[string]bool, len(dc.Chunker.TitleStopwords))
	for _, w := range dc.Chunker.TitleStopwords {
		stops[strings.ToLower(w)] = true
	}

	// The fold function for title comparison is derived from the same
	// diacritic mode used by the lexical index.
	foldFn := makeTitleFoldFn(dc.Resolution.FoldDiacritics)
	stripStem := dc.Chunker.StripTitleStemVowel

	return &fusionConfig{
		router: router,
		grouper: func() *workGrouper {
			return newWorkGrouper(stops, foldFn, stripStem)
		},
		foldTitle: foldFn,
	}
}

// makeTitleFoldFn returns a fold function for title comparison based on the
// configured DiacriticMode. This keeps title normalisation consistent with
// the BM25 index fold so the same corpus produces matching tokens.
func makeTitleFoldFn(mode agentconfig.DiacriticMode) func(string) string {
	// Reuse the same fold tables from the lexical package by delegating through
	// a temporary index instance. This avoids duplicating the fold tables here.
	tmp := lexical.NewWithFold(mode)
	return tmp.Fold
}

// selectDiverse fills topK slots, capping how many may come from one work while
// letting a dominant work take the whole slate when the evidence really is
// concentrated there.
//
// The previous rule capped every source at (topK+1)/2 unconditionally, which
// for a focused question about a single text — the normal case — discarded the
// best-ranked chunks in favour of lower-ranked ones from other books.
func selectDiverse(ranked []*candidate, topK int, fc *fusionConfig) []*candidate {
	if len(ranked) <= topK {
		return ranked
	}

	g := fc.grouper()

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
