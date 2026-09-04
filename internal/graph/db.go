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
	"unicode/utf8"

	"github.com/cayleygraph/cayley"
	"github.com/cayleygraph/cayley/graph"
	_ "github.com/cayleygraph/cayley/graph/kv/bolt"
	_ "github.com/cayleygraph/cayley/graph/memstore"
	"github.com/cayleygraph/quad"

	"github.com/akashicode/kash/internal/llm"
)

// Triple represents a Subject-Predicate-Object triple.
type Triple = llm.Triple

// SearchResult represents a result from a graph search.
type SearchResult struct {
	Subject   string  `json:"subject"`
	Predicate string  `json:"predicate"`
	Object    string  `json:"object"`
	Source    string  `json:"source,omitempty"`
	ChunkID   string  `json:"chunk_id,omitempty"`
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
	// weight counts how many raw quads collapsed into each deduplicated triple,
	// keyed on FoldKey(S,P,O). A triple mentioned in N chunks has weight N.
	weight map[string]int
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

	s = &snapshot{byEntity: map[string][]int{}, weight: map[string]int{}}
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
		// Count every raw quad occurrence — including duplicates — before
		// deduplication, so weight reflects how many chunks attest this fact.
		s.weight[key]++
		if seen[key] {
			continue
		}
		seen[key] = true

		i := len(s.triples)
		src, chkID := parseLabel(quadValueStr(q.Label))
		s.triples = append(s.triples, SearchResult{
			Subject:   subj,
			Predicate: pred,
			Object:    obj,
			Source:    src,
			ChunkID:   chkID,
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

// AllTriples returns all unique triples currently stored in the graph.
func (db *DB) AllTriples(ctx context.Context) []SearchResult {
	s := db.index(ctx)
	out := make([]SearchResult, len(s.triples))
	copy(out, s.triples)
	return out
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

		var quadLabel interface{}
		src := normalise(source)
		chk := strings.TrimSpace(t.ChunkID)
		if src != "" && chk != "" {
			quadLabel = src + "|" + chk
		} else if chk != "" {
			quadLabel = chk
		} else if src != "" {
			quadLabel = src
		}

		quads = append(quads, quad.Make(subj, pred, obj, quadLabel))
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
	return db.SearchWithSeeds(ctx, query, nil, nil, topK, maxHops)
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
		src, chkID := parseLabel(quadValueStr(q.Label))
		sr := SearchResult{
			Subject:   quadValueStr(q.Subject),
			Predicate: quadValueStr(q.Predicate),
			Object:    quadValueStr(q.Object),
			Source:    src,
			ChunkID:   chkID,
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
		src, _ := parseLabel(quadValueStr(q.Label))
		if src == source || quadValueStr(q.Label) == source {
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
			src, chkID := parseLabel(quadValueStr(q.Label))
			results = append(results, SearchResult{
				Subject:   subj,
				Predicate: pred,
				Object:    obj,
				Source:    src,
				ChunkID:   chkID,
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

// parseLabel extracts the source document and optional chunk ID from a quad label.
// Labels are formatted as "source|chunk_id", or just "source" for older/legacy quads.
func parseLabel(labelStr string) (source, chunkID string) {
	if idx := strings.Index(labelStr, "|"); idx >= 0 {
		return labelStr[:idx], labelStr[idx+1:]
	}
	return labelStr, ""
}

// FormatResults converts graph search results into a readable context string.
func FormatResults(results []SearchResult) string {
	return FormatResultsWithPassages(results, nil)
}

// FormatResultsWithPassages converts graph search results into a readable context string,
// citing the 1-based passage number if a fact's ChunkID matches a retrieved passage.
func FormatResultsWithPassages(results []SearchResult, chunkPassageMap map[string]int) string {
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

		// Cite a passage only when the supporting chunk is one the reader can
		// actually see. A chunk ID is an internal identifier — "tantra_md_312"
		// tells a reader nothing and cannot be looked up — so when the
		// supporting chunk was not retrieved, the fact degrades to a
		// document-level citation. Printing the ID anyway put a string that
		// looks like a reference in front of a model told to cite inline,
		// which is an invitation to quote it as one.
		citation := ""
		if r.ChunkID != "" && chunkPassageMap != nil {
			if pNum, ok := chunkPassageMap[r.ChunkID]; ok {
				if r.Source != "" {
					citation = fmt.Sprintf(" (source: %s [passage %d])", r.Source, pNum)
				} else {
					citation = fmt.Sprintf(" [passage %d]", pNum)
				}
			}
		}
		if citation == "" && r.Source != "" {
			citation = fmt.Sprintf(" (source: %s)", r.Source)
		}

		fmt.Fprintf(&sb, "%s%s %s %s%s%s\n", prefix, r.Subject, r.Predicate, r.Object, citation, suffix)
	}
	return sb.String()
}

// TripleWeight returns how many raw quads (across all chunks and documents)
// collapsed into the given canonical triple. Returns 1 when the triple is
// present but was only seen once, and 1 (not 0) when the triple is absent —
// so callers can safely use the value as a multiplicative factor.
func (db *DB) TripleWeight(subject, predicate, object string) int {
	s := db.index(context.Background())
	if s == nil {
		return 1
	}
	key := FoldKey(subject, predicate, object)
	if w := s.weight[key]; w > 0 {
		return w
	}
	return 1
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
		// Measured in runes, not bytes. len() counts bytes, so this discarded
		// short Latin terms like "om" and "ka" while admitting any single
		// Devanagari character (3 bytes) — backwards for a Sanskrit corpus.
		if utf8.RuneCountInString(term) < 2 {
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
