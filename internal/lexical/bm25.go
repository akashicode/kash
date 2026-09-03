// Package lexical provides a pure-Go BM25 index over chunk text.
//
// Dense embeddings cannot match exact tokens. A query like "dharana 49" is a
// keyword plus a number: the number carries almost no semantic signal, so
// cosine similarity ranks unrelated but term-dense pages above the passage that
// literally contains the heading. BM25 scores that query correctly, and fusing
// the two routes recovers what either alone misses.
package lexical

import (
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// BM25 tuning. These are the standard defaults and are not worth tuning before
// there is an evaluation set to tune against.
const (
	k1 = 1.2
	b  = 0.75
)

// FileName is the index file written next to the vector store.
const FileName = "lexical.idx"

// posting records a term's frequency within one document.
type posting struct {
	Doc  uint32
	Freq uint32
}

// Result is one scored document.
type Result struct {
	ID       string
	Score    float64
	Metadata map[string]string
}

// Index is a BM25 index over chunk text plus the chunk metadata needed for
// exact-match lookups.
type Index struct {
	// IDs maps internal document number to chunk ID.
	IDs []string
	// Meta holds each document's stored metadata, parallel to IDs.
	Meta []map[string]string
	// Postings maps a term to the documents containing it.
	Postings map[string][]posting
	// Lengths is each document's token count.
	Lengths []uint32
	// AvgLen is the mean document length.
	AvgLen float64
}

// New returns an empty index.
func New() *Index {
	return &Index{Postings: map[string][]posting{}}
}

// Tokenize lowercases text and splits it into alphanumeric terms.
//
// Numbers are kept as terms — they are exactly what makes a verse or dhāraṇā
// number findable. The minimum length is measured in runes, not bytes: a byte
// check silently discards short Latin terms like "om" while admitting any
// single Devanagari character, which is backwards for this corpus.
func Tokenize(text string) []string {
	fields := strings.FieldsFunc(Fold(strings.ToLower(text)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := fields[:0]
	for _, f := range fields {
		if len([]rune(f)) >= 2 {
			out = append(out, f)
		}
	}
	return out
}

// iastFolds maps the IAST diacritics used throughout this corpus to ASCII.
//
// Folding is not cosmetic. The same word is written "dhāraṇā" in a Sanskrit
// edition and typed "dharana" by a reader; without folding those are different
// terms and the query simply does not match the passage it names. The same goes
// for vijñāna/vijnana and śiva/shiva/siva.
var iastFolds = strings.NewReplacer(
	"ā", "a", "ī", "i", "ū", "u", "ṛ", "r", "ṝ", "r", "ḷ", "l", "ḹ", "l",
	"ṅ", "n", "ñ", "n", "ṇ", "n", "ṃ", "m", "ṁ", "m", "ṉ", "n",
	"ṭ", "t", "ḍ", "d", "ś", "s", "ṣ", "s", "ḥ", "h", "ḻ", "l",
	"é", "e", "è", "e", "ê", "e", "ô", "o", "ö", "o", "ü", "u",
	"ā̆", "a", "‘", "", "’", "", "ʼ", "",
)

// Fold normalizes IAST diacritics to ASCII so transliteration variants of a
// term match each other.
func Fold(s string) string { return iastFolds.Replace(s) }

// Add indexes one chunk.
func (ix *Index) Add(id, content string, meta map[string]string) {
	docNum := uint32(len(ix.IDs))
	ix.IDs = append(ix.IDs, id)
	ix.Meta = append(ix.Meta, meta)

	freqs := map[string]uint32{}
	terms := Tokenize(content)
	for _, t := range terms {
		freqs[t]++
	}
	for t, f := range freqs {
		ix.Postings[t] = append(ix.Postings[t], posting{Doc: docNum, Freq: f})
	}
	ix.Lengths = append(ix.Lengths, uint32(len(terms)))
}

// Finalize computes derived statistics. Call once after the last Add.
func (ix *Index) Finalize() {
	var total uint64
	for _, l := range ix.Lengths {
		total += uint64(l)
	}
	if len(ix.Lengths) > 0 {
		ix.AvgLen = float64(total) / float64(len(ix.Lengths))
	}
}

// Len returns the number of indexed documents.
func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.IDs)
}

// Search returns the top-k documents for a query, ranked by BM25.
func (ix *Index) Search(query string, k int) []Result {
	if ix == nil || len(ix.IDs) == 0 || k <= 0 {
		return nil
	}

	n := float64(len(ix.IDs))
	scores := map[uint32]float64{}

	for _, term := range Tokenize(query) {
		postings, ok := ix.Postings[term]
		if !ok {
			continue
		}
		df := float64(len(postings))
		// Robertson/Sparck-Jones IDF with the +1 that keeps it non-negative.
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))

		for _, p := range postings {
			tf := float64(p.Freq)
			dl := float64(ix.Lengths[p.Doc])
			norm := tf + k1*(1-b+b*dl/ix.AvgLen)
			if norm == 0 {
				continue
			}
			scores[p.Doc] += idf * (tf * (k1 + 1)) / norm
		}
	}

	out := make([]Result, 0, len(scores))
	for doc, score := range scores {
		out = append(out, Result{ID: ix.IDs[doc], Score: score, Metadata: ix.Meta[doc]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// FindByRef returns documents whose metadata lists the given value under field.
// Metadata values may be comma-separated lists, because one chunk can cover
// several verses.
func (ix *Index) FindByRef(field, value string) []Result {
	if ix == nil || value == "" {
		return nil
	}
	var out []Result
	for i, meta := range ix.Meta {
		for _, v := range strings.Split(meta[field], ",") {
			if strings.TrimSpace(v) == value {
				out = append(out, Result{ID: ix.IDs[i], Score: 1, Metadata: meta})
				break
			}
		}
	}
	return out
}

// Save writes the index to a file atomically.
func (ix *Index) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create index dir: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".lexical-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp index: %w", err)
	}
	tmpPath := tmp.Name()

	if err := gob.NewEncoder(tmp).Encode(ix); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp index: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace index: %w", err)
	}
	return nil
}

// Load reads an index from disk. A missing file yields an empty index rather
// than an error, so a corpus built before the lexical index existed still
// serves — with vector search alone.
func Load(path string) (*Index, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("open index %q: %w", path, err)
	}
	defer f.Close()

	ix := New()
	if err := gob.NewDecoder(f).Decode(ix); err != nil {
		return nil, fmt.Errorf("decode index %q: %w", path, err)
	}
	if ix.Postings == nil {
		ix.Postings = map[string][]posting{}
	}
	return ix, nil
}
