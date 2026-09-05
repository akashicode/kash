// Package lexical provides a pure-Go BM25 index over chunk text.
//
// Dense embeddings cannot match exact tokens. A query like "section 4.2" is a
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

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/fsutil"
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

// FoldFunc normalises a string before indexing or querying. It is applied
// inside Tokenize so callers do not need to pre-fold their text.
type FoldFunc func(string) string

// latinFolds maps common European diacritics to plain ASCII.
// Safe to apply for any Latin-script language.
var latinFolds = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n", "ý", "y", "ÿ", "y",
	"š", "s", "ž", "z", "ð", "d", "þ", "t",
)

// iastFolds maps IAST/ITRANS transliteration characters to plain ASCII.
// Apply for Sanskrit corpora where readers type "dharana" but texts use
// "dhāraṇā". Folding unites query tokens with index tokens.
var iastFolds = strings.NewReplacer(
	"ā", "a", "ī", "i", "ū", "u", "ṛ", "r", "ṝ", "r", "ḷ", "l", "ḹ", "l",
	"ṅ", "n", "ñ", "n", "ṇ", "n", "ṃ", "m", "ṁ", "m", "ṉ", "n",
	"ṭ", "t", "ḍ", "d", "ś", "s", "ṣ", "s", "ḥ", "h", "ḻ", "l",
	"ā̆", "a", "‘", "", "’", "", "ʼ", "", "'", "",
)

// makeFoldFunc returns a FoldFunc for the given DiacriticMode.
func makeFoldFunc(mode config.DiacriticMode) FoldFunc {
	switch mode {
	case config.DiacriticNone:
		return func(s string) string { return s }
	case config.DiacriticIAST:
		return iastFolds.Replace
	case config.DiacriticBoth:
		return func(s string) string { return latinFolds.Replace(iastFolds.Replace(s)) }
	default: // DiacriticLatin and anything unrecognised
		return latinFolds.Replace
	}
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
	// FoldMode records the diacritic mode this index was BUILT with, and is
	// persisted.
	//
	// The index and the queries run against it must tokenise identically. When
	// the mode lived only in configuration, a corpus built with IAST folding
	// and served with a missing or changed config would tokenise queries
	// differently from the index: keyword search would return nothing, with no
	// error and no log line. Storing it here makes that impossible.
	//
	// Absent in indexes written before this field existed, where gob leaves it
	// empty and the caller's configured mode still applies.
	FoldMode config.DiacriticMode
	// fold is the compiled form of FoldMode, rebuilt on load.
	fold FoldFunc `gob:"-"`
}

// New returns an empty index with the default fold (Latin diacritics only).
func New() *Index {
	return &Index{
		Postings: map[string][]posting{},
		FoldMode: config.DiacriticLatin,
		fold:     makeFoldFunc(config.DiacriticLatin),
	}
}

// NewWithFold returns an empty index that uses the given DiacriticMode when
// tokenising. Call this instead of New when the agent.yaml specifies a mode.
func NewWithFold(mode config.DiacriticMode) *Index {
	return &Index{
		Postings: map[string][]posting{},
		FoldMode: mode,
		fold:     makeFoldFunc(mode),
	}
}

// SetFold replaces the fold function on a loaded index. Call this after Load
// to restore the corpus-specific normalisation that is not persisted in the
// index file itself.
func (ix *Index) SetFold(mode config.DiacriticMode) {
	ix.FoldMode = mode
	ix.fold = makeFoldFunc(mode)
}

// Fold normalises s with the index's diacritic fold function.
// Exported so callers that pre-process queries can apply the same transform
// before passing terms to FindByRef.
func (ix *Index) Fold(s string) string {
	if ix.fold == nil {
		return s
	}
	return ix.fold(s)
}

// Tokenize lowercases text, applies the index's fold, and splits it into
// alphanumeric terms.
//
// Numbers are kept as terms — they are exactly what makes a section or verse
// number findable. The minimum length is measured in runes, not bytes: a byte
// check silently discards short Latin terms like "om" while admitting any
// single Devanagari character, which is backwards for a Sanskrit corpus.
func (ix *Index) Tokenize(text string) []string {
	folded := ix.Fold(strings.ToLower(text))
	fields := strings.FieldsFunc(folded, func(r rune) bool {
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

// Tokenize is a package-level helper that uses Latin-only folding (the safe
// default). Prefer ix.Tokenize when you have an Index instance.
func Tokenize(text string) []string {
	tmp := New()
	return tmp.Tokenize(text)
}

// Fold is a package-level helper that applies Latin-only diacritic folding.
// Prefer ix.Fold when you have an Index instance.
func Fold(s string) string { return latinFolds.Replace(s) }

// Add indexes one chunk.
func (ix *Index) Add(id, content string, meta map[string]string) {
	docNum := uint32(len(ix.IDs))
	ix.IDs = append(ix.IDs, id)
	ix.Meta = append(ix.Meta, meta)

	freqs := map[string]uint32{}
	terms := ix.Tokenize(content)
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

	for _, term := range ix.Tokenize(query) {
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
// several sections or verses.
func (ix *Index) FindByRef(field, value string) []Result {
	if ix == nil || value == "" {
		return nil
	}
	value = chunker.NormalizeRefValue(value)
	var out []Result
	for i, meta := range ix.Meta {
		for _, v := range strings.Split(meta[field], ",") {
			if chunker.NormalizeRefValue(v) == value {
				out = append(out, Result{ID: ix.IDs[i], Score: 1, Metadata: meta})
				break
			}
		}
	}
	return out
}

// FindByAnyRef returns documents carrying the given value under any reference
// key, whatever that key is called.
//
// It exists because the two sides of a reference lookup name the key
// independently. The chunker names it from the pattern that matched the
// *document*; the query router names it from the pattern that matched the
// *query*. Those are different texts, so the names need not agree even when
// they mean the same passage — a corpus that writes "97)" in one place and
// "Verse 97" in another indexes the first as a section and the second as a
// verse, and a reader asking for verse 97 matches only half of it.
//
// Infrastructure keys are excluded: they hold prose, not references, so a
// heading containing the number would otherwise match.
func (ix *Index) FindByAnyRef(value string) []Result {
	if ix == nil || value == "" {
		return nil
	}
	value = chunker.NormalizeRefValue(value)
	var out []Result
	for i, meta := range ix.Meta {
		if matchesAnyRef(meta, value) {
			out = append(out, Result{ID: ix.IDs[i], Score: 1, Metadata: meta})
		}
	}
	return out
}

// refInfraKeys are the metadata keys the chunker owns. They describe the chunk
// rather than number it, so they never carry a reference value.
var refInfraKeys = map[string]bool{
	chunker.MetaBook:        true,
	chunker.MetaHeading:     true,
	chunker.MetaBreadcrumb:  true,
	chunker.MetaContentType: true,
	chunker.MetaNoiseScore:  true,
	"source":                true,
}

func matchesAnyRef(meta map[string]string, value string) bool {
	for key, vals := range meta {
		if refInfraKeys[key] {
			continue
		}
		for _, v := range strings.Split(vals, ",") {
			if chunker.NormalizeRefValue(v) == value {
				return true
			}
		}
	}
	return false
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
	if err := fsutil.ReplaceFile(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace index: %w", err)
	}
	return nil
}

// Load reads an index from disk. A missing file yields an empty index rather
// than an error, so a corpus built before the lexical index existed still
// serves — with vector search alone.
//
// The fold function is NOT persisted. Call ix.SetFold(mode) after loading to
// restore the corpus-specific diacritic normalisation.
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
	// Rebuild the fold from the mode the index was written with, so an index
	// always tokenises queries the way it tokenised its own documents. An index
	// written before FoldMode existed decodes as empty and keeps New()'s Latin
	// default, which is what those indexes were built with.
	if !ix.FoldMode.Valid() {
		ix.FoldMode = config.DiacriticLatin
	}
	ix.fold = makeFoldFunc(ix.FoldMode)
	return ix, nil
}
