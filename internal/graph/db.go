package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// AddTriples inserts a batch of triples into the graph.
func (db *DB) AddTriples(ctx context.Context, triples []Triple) error {
	if len(triples) == 0 {
		return nil
	}

	quads := make([]quad.Quad, 0, len(triples))
	for _, t := range triples {
		if t.Subject == "" || t.Predicate == "" || t.Object == "" {
			continue
		}
		quads = append(quads, quad.Make(
			normalise(t.Subject),
			normalise(t.Predicate),
			normalise(t.Object),
			nil,
		))
	}

	if err := db.store.AddQuadSet(quads); err != nil {
		return fmt.Errorf("add quads: %w", err)
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

	queryTerms := strings.Fields(strings.ToLower(query))
	results := []SearchResult{}
	seen := map[string]bool{}

	it := db.store.QuadsAllIterator()
	defer it.Close()

	for it.Next(ctx) {
		ref := it.Result()
		q := db.store.Quad(ref)

		subj := quadValueStr(q.Subject)
		pred := quadValueStr(q.Predicate)
		obj := quadValueStr(q.Object)

		key := subj + "|" + pred + "|" + obj
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
				Score:     score,
			})
		}

		if len(results) >= topK*3 {
			break
		}
	}

	// Sort by score descending
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].Score > results[j-1].Score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}

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
		sb.WriteString(fmt.Sprintf("- %s %s %s\n", r.Subject, r.Predicate, r.Object))
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

func scoreMatch(terms []string, values ...string) float64 {
	combined := strings.ToLower(strings.Join(values, " "))
	score := 0.0
	for _, term := range terms {
		if len(term) < 3 {
			continue
		}
		if strings.Contains(combined, term) {
			score += 1.0
		}
	}
	return score
}
