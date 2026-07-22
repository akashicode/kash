package graph

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
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
}

// DB wraps a cayley graph database.
type DB struct {
	store *cayley.Handle
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
	return nil
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
		if r.Source != "" {
			fmt.Fprintf(&sb, "- %s %s %s (source: %s)\n", r.Subject, r.Predicate, r.Object, r.Source)
		} else {
			fmt.Fprintf(&sb, "- %s %s %s\n", r.Subject, r.Predicate, r.Object)
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
