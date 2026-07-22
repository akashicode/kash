package graph

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/cayleygraph/cayley"
	"github.com/cayleygraph/cayley/graph"
	_ "github.com/cayleygraph/cayley/graph/kv/bolt"
	_ "github.com/cayleygraph/cayley/graph/memstore"
	"github.com/cayleygraph/quad"

	"github.com/akashicode/kash/internal/llm"
)

// ErrNotFound is returned when no graph results are found.
var ErrNotFound = errors.New("no graph results found")

// Triple represents a Subject-Predicate-Object triple.
type Triple = llm.Triple

// SearchResult represents a result from a graph search.
type SearchResult struct {
	Subject   string  `json:"subject"`
	Predicate string  `json:"predicate"`
	Object    string  `json:"object"`
	Source    string  `json:"source,omitempty"`
	Score     float64 `json:"score"`
	// Hop is 0 for a fact that matched the query directly, and 1 for a fact
	// reached by traversing one edge from a direct match.
	Hop int `json:"hop"`
	// Via names the entity that connects a hop-1 fact back to its seed fact.
	Via string `json:"via,omitempty"`
}

// DB wraps a cayley graph database.
type DB struct {
	store *cayley.Handle

	// idx caches an in-memory snapshot with an entity adjacency index, so
	// traversal does not re-scan the quad store on every query. Built lazily
	// and dropped whenever the graph is mutated.
	mu  sync.RWMutex
	idx *snapshot

	// aliases optionally folds entity spelling variants together during
	// traversal. Nil means no entity resolution, which is a valid state.
	aliases *AliasSet
}

// SetAliases installs an entity resolution map. Passing nil disables entity
// resolution. The traversal index is rebuilt on next use.
func (db *DB) SetAliases(a *AliasSet) {
	db.mu.Lock()
	db.aliases = a
	db.idx = nil
	db.mu.Unlock()
}

// entityKey returns the adjacency key for an entity, folding it through the
// alias map when one is loaded. Without aliases this is just the normalized
// surface form, so behaviour is unchanged.
func (db *DB) entityKey(entity string) string {
	db.mu.RLock()
	a := db.aliases
	db.mu.RUnlock()
	return a.Resolve(entity)
}

// snapshot is an in-memory copy of the graph plus an entity->triple index.
type snapshot struct {
	triples  []SearchResult
	byEntity map[string][]int
}

// invalidate drops the cached index after a mutation.
func (db *DB) invalidate() {
	db.mu.Lock()
	db.idx = nil
	db.mu.Unlock()
}

// index returns the cached snapshot, building it on first use.
func (db *DB) index(ctx context.Context) *snapshot {
	db.mu.RLock()
	s := db.idx
	db.mu.RUnlock()
	if s != nil {
		return s
	}

	db.mu.Lock()
	defer db.mu.Unlock()
	if db.idx != nil {
		return db.idx
	}

	s = &snapshot{byEntity: map[string][]int{}}
	seen := map[string]bool{}
	aliases := db.aliases // already holding the write lock

	it := db.store.QuadsAllIterator()
	defer it.Close()
	for it.Next(ctx) {
		q := db.store.Quad(it.Result())
		subj := quadValueStr(q.Subject)
		pred := quadValueStr(q.Predicate)
		obj := quadValueStr(q.Object)
		if subj == "" || pred == "" || obj == "" {
			continue
		}
		key := FoldKey(subj, pred, obj)
		if seen[key] {
			continue
		}
		seen[key] = true

		i := len(s.triples)
		s.triples = append(s.triples, SearchResult{
			Subject:   subj,
			Predicate: pred,
			Object:    obj,
			Source:    quadValueStr(q.Label),
		})
		// Index under the alias-resolved key so spelling variants
		// (Gorakhnath / Gorakhnatha) share one adjacency bucket and chains
		// traverse across them.
		fs, fo := aliases.Resolve(subj), aliases.Resolve(obj)
		s.byEntity[fs] = append(s.byEntity[fs], i)
		if fo != fs {
			s.byEntity[fo] = append(s.byEntity[fo], i)
		}
	}

	db.idx = s
	return s
}

// NewDB creates a new in-memory graph DB.
func NewDB() (*DB, error) {
	store, err := cayley.NewMemoryGraph()
	if err != nil {
		return nil, fmt.Errorf("create memory graph: %w", err)
	}
	return &DB{store: store}, nil
}

// NewDBFromPath opens a persistent bolt-backed cayley graph.
func NewDBFromPath(path string) (*DB, error) {
	if err := graph.InitQuadStore("bolt", path, nil); err != nil {
		if !strings.Contains(err.Error(), "already") {
			return nil, fmt.Errorf("init bolt quad store at %q: %w", path, err)
		}
	}

	store, err := cayley.NewGraph("bolt", path, nil)
	if err != nil {
		return nil, fmt.Errorf("open bolt graph at %q: %w", path, err)
	}
	return &DB{store: store}, nil
}

// AddTriples inserts a batch of triples into the graph. The source document
// name is stored as the quad label so retrieved facts can cite their origin;
// pass "" when the source is unknown.
func (db *DB) AddTriples(ctx context.Context, triples []Triple, source string) error {
	if len(triples) == 0 {
		return nil
	}

	var label interface{}
	if src := normalise(source); src != "" {
		label = src
	}

	quads := make([]quad.Quad, 0, len(triples))
	seen := map[string]bool{}
	for _, t := range triples {
		subj := NormalizeEntity(t.Subject)
		pred := CanonicalPredicate(t.Predicate)
		obj := NormalizeEntity(t.Object)

		if subj == "" || pred == "" || obj == "" {
			continue
		}
		// Drop junk and commercial metadata rather than spending retrieval
		// budget on "Randhir Book Sales distributes X"
		if !hasLetters(subj) || !hasLetters(obj) {
			continue
		}
		if IsNoisePredicate(t.Predicate) || looksLikeMetadataEntity(subj) || looksLikeMetadataEntity(obj) {
			continue
		}
		// A self-referential triple carries no information
		if normalizeSurface(subj) == normalizeSurface(obj) {
			continue
		}
		// Collapse near-duplicates that differ only in case or predicate wording
		key := FoldKey(subj, pred, obj)
		if seen[key] {
			continue
		}
		seen[key] = true

		quads = append(quads, quad.Make(subj, pred, obj, label))
	}

	if len(quads) == 0 {
		return nil
	}
	if err := db.store.AddQuadSet(quads); err != nil {
		return fmt.Errorf("add quads: %w", err)
	}
	db.invalidate()
	return nil
}

// Traversal tuning. These bound the expansion so a query touching a hub
// entity (the corpus has ~318 entities with degree >= 20) cannot flood the
// results with everything attached to it.
const (
	// hopDecay scales a hop-1 fact's score relative to the seed it came from,
	// so direct matches always outrank traversed ones.
	hopDecay = 0.45
	// maxHubDegree skips expansion through entities so generic that their
	// neighbours carry no signal.
	maxHubDegree = 150
	// maxPerEntity caps how many neighbours a single connecting entity may
	// contribute, so one well-connected node cannot dominate.
	maxPerEntity = 4
	// maxSeedsToExpand limits how many of the best direct matches are expanded.
	maxSeedsToExpand = 8
	// hopSharePct reserves part of the result budget for traversed facts.
	// Without a reserved share, direct matches always fill topK on a corpus of
	// any size and traversal never surfaces anything.
	hopSharePct = 35
)

// SearchWithHops runs a graph search and then expands one hop from the
// best-matching facts, surfacing connected chains (A taught B, B founded C)
// rather than only the isolated facts that matched the query text.
//
// Direct matches are always ranked above traversed facts. Set maxHops to 0 for
// the flat behaviour of Search.
func (db *DB) SearchWithHops(ctx context.Context, query string, topK, maxHops int) ([]SearchResult, error) {
	if query == "" {
		return nil, errors.New("query cannot be empty")
	}
	if topK <= 0 {
		topK = 10
	}

	seeds, err := db.Search(ctx, query, topK)
	if err != nil {
		return nil, err
	}
	if maxHops <= 0 || len(seeds) == 0 {
		return seeds, nil
	}

	snap := db.index(ctx)
	seen := map[string]bool{}
	for _, s := range seeds {
		seen[FoldKey(s.Subject, s.Predicate, s.Object)] = true
	}

	expandFrom := seeds
	if len(expandFrom) > maxSeedsToExpand {
		expandFrom = expandFrom[:maxSeedsToExpand]
	}

	var expanded []SearchResult
	for _, seed := range expandFrom {
		for _, entity := range []string{seed.Subject, seed.Object} {
			key := db.entityKey(entity)
			neighbours := snap.byEntity[key]
			// Skip hubs: too generic for their neighbours to mean anything
			if len(neighbours) == 0 || len(neighbours) > maxHubDegree {
				continue
			}

			added := 0
			for _, i := range neighbours {
				if added >= maxPerEntity {
					break
				}
				t := snap.triples[i]
				fk := FoldKey(t.Subject, t.Predicate, t.Object)
				if seen[fk] {
					continue
				}
				seen[fk] = true

				t.Hop = 1
				t.Via = entity
				// Rank below its seed, and prefer connections through more
				// specific (lower-degree) entities.
				t.Score = seed.Score * hopDecay * (1 - float64(len(neighbours))/float64(maxHubDegree)*0.3)
				expanded = append(expanded, t)
				added++
			}
		}
	}

	sort.SliceStable(expanded, func(i, j int) bool {
		return expanded[i].Score > expanded[j].Score
	})

	// Traversed facts are ADDITIVE — they never displace a direct match.
	// Evicting a directly-matching fact (say "X was disciple of Y") to make
	// room for a one-hop fact is a regression, since the direct match is by
	// construction the more relevant one.
	hopBudget := topK * hopSharePct / 100
	if hopBudget == 0 && topK > 1 && len(expanded) > 0 {
		hopBudget = 1
	}
	if hopBudget > len(expanded) {
		hopBudget = len(expanded)
	}

	out := make([]SearchResult, 0, len(seeds)+hopBudget)
	out = append(out, seeds...)
	out = append(out, expanded[:hopBudget]...)
	return out, nil
}

// Sample returns up to limit triples drawn uniformly from the whole graph
// (reservoir sampling), so a visualization is not biased toward whichever
// document sits first in storage order.
func (db *DB) Sample(ctx context.Context, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 100
	}

	samples := make([]SearchResult, 0, limit)
	n := 0

	it := db.store.QuadsAllIterator()
	defer it.Close()

	for it.Next(ctx) {
		q := db.store.Quad(it.Result())
		sr := SearchResult{
			Subject:   quadValueStr(q.Subject),
			Predicate: quadValueStr(q.Predicate),
			Object:    quadValueStr(q.Object),
			Source:    quadValueStr(q.Label),
		}
		n++
		if len(samples) < limit {
			samples = append(samples, sr)
		} else if j := rand.Intn(n); j < limit {
			samples[j] = sr
		}
	}
	return samples, nil
}

// DeleteBySource removes all triples whose label matches the given source
// document. Used by incremental builds to replace a changed document's facts.
func (db *DB) DeleteBySource(ctx context.Context, source string) error {
	if source == "" {
		return errors.New("source cannot be empty")
	}

	var toRemove []quad.Quad
	it := db.store.QuadsAllIterator()
	for it.Next(ctx) {
		q := db.store.Quad(it.Result())
		if quadValueStr(q.Label) == source {
			toRemove = append(toRemove, q)
		}
	}
	it.Close()

	for _, q := range toRemove {
		if err := db.store.RemoveQuad(q); err != nil {
			return fmt.Errorf("remove quad for source %q: %w", source, err)
		}
	}
	db.invalidate()
	return nil
}

// Search queries the graph for entities related to the query terms.
func (db *DB) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	if query == "" {
		return nil, errors.New("query cannot be empty")
	}
	if topK <= 0 {
		topK = 10
	}

	queryTerms := tokenize(query)
	results := []SearchResult{}
	seen := map[string]bool{}

	it := db.store.QuadsAllIterator()
	defer it.Close()

	// Scan the FULL graph — an early exit would bias results toward whichever
	// documents happen to sit first in storage order, making later documents
	// unreachable. All matches are collected, then sorted by score.
	for it.Next(ctx) {
		ref := it.Result()
		q := db.store.Quad(ref)

		subj := quadValueStr(q.Subject)
		pred := quadValueStr(q.Predicate)
		obj := quadValueStr(q.Object)

		// Fold on a case- and predicate-normalized key so facts already in the
		// graph that differ only in wording ("has X" vs "contains X") collapse
		// to one result — no rebuild required.
		key := FoldKey(subj, pred, obj)
		if seen[key] {
			continue
		}

		score := scoreMatch(queryTerms, subj, pred, obj)
		if score > 0 {
			seen[key] = true
			results = append(results, SearchResult{
				Subject:   subj,
				Predicate: pred,
				Object:    obj,
				Source:    quadValueStr(q.Label),
				Score:     score,
			})
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// FormatResults converts graph search results into a readable context string.
func FormatResults(results []SearchResult) string {
	if len(results) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Knowledge Graph Facts:\n")
	for _, r := range results {
		// Mark traversed facts so the model can tell a direct match from a
		// connected one, and knows which entity links them.
		prefix := "- "
		suffix := ""
		if r.Hop > 0 {
			prefix = "  ↳ "
			if r.Via != "" {
				suffix = fmt.Sprintf(" [connected via %s]", r.Via)
			}
		}
		if r.Source != "" {
			fmt.Fprintf(&sb, "%s%s %s %s (source: %s)%s\n", prefix, r.Subject, r.Predicate, r.Object, r.Source, suffix)
		} else {
			fmt.Fprintf(&sb, "%s%s %s %s%s\n", prefix, r.Subject, r.Predicate, r.Object, suffix)
		}
	}
	return sb.String()
}

// Count returns the number of quads in the graph.
func (db *DB) Count() int64 {
	stats, err := db.store.Stats(context.Background(), false)
	if err != nil {
		return 0
	}
	return stats.Quads.Size
}

// Close shuts down the graph store.
func (db *DB) Close() error {
	return db.store.Close()
}

func normalise(s string) string {
	return strings.TrimSpace(s)
}

func quadValueStr(v quad.Value) string {
	if v == nil {
		return ""
	}
	s := quad.StringOf(v)
	s = strings.TrimPrefix(s, "\"")
	s = strings.TrimSuffix(s, "\"")
	return strings.TrimSpace(s)
}

// tokenize lowercases text and splits it into alphanumeric terms, stripping
// punctuation so "yoga?" matches "yoga".
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// scoreMatch scores a triple against query terms. Whole-word matches score
// higher than substring matches so precise entity hits outrank incidental
// overlaps (e.g. "art" inside "particular").
func scoreMatch(terms []string, values ...string) float64 {
	combined := strings.ToLower(strings.Join(values, " "))
	words := map[string]bool{}
	for _, w := range tokenize(combined) {
		words[w] = true
	}

	score := 0.0
	for _, term := range terms {
		if len(term) < 3 {
			continue
		}
		switch {
		case words[term]:
			score += 2.0
		case strings.Contains(combined, term):
			score += 1.0
		}
	}
	return score
}
