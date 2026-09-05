// Package manifest tracks build state so `kash build` can run incrementally,
// resume interrupted builds, and version the compiled corpus.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/akashicode/kash/internal/fsutil"
)

// FileName is the manifest file name inside the agent's data/ directory.
const FileName = "build.manifest.json"

// DocState records the build progress of a single source document.
type DocState struct {
	// SHA256 is the content hash used to detect changed documents.
	SHA256 string `json:"sha256"`
	// Chunks is the number of chunks produced from this document.
	Chunks int `json:"chunks"`
	// Triples is the number of graph triples extracted from this document.
	Triples int `json:"triples"`
	// VectorDone marks the embedding phase complete.
	VectorDone bool `json:"vector_done"`
	// GraphBatchesDone counts completed triple-extraction batches, enabling
	// resume mid-document after an interrupted build.
	GraphBatchesDone int `json:"graph_batches_done"`
	// GraphDone marks the triple-extraction phase complete.
	GraphDone bool `json:"graph_done"`
	// CompletedAt is when both phases finished.
	CompletedAt time.Time `json:"completed_at,omitzero"`
}

// Done reports whether the document is fully built.
func (d *DocState) Done() bool {
	return d.VectorDone && d.GraphDone
}

// Manifest records the state of a compiled corpus.
type Manifest struct {
	// Version is the corpus version, bumped on every build that changes data.
	Version int `json:"version"`
	// UpdatedAt is when the manifest was last written.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// EmbedModel and EmbedDimensions pin the embedder the corpus was built
	// with — mixing embeddings from different models is invalid.
	EmbedModel      string `json:"embed_model"`
	EmbedDimensions int    `json:"embed_dimensions"`
	// ChunkSize and ChunkOverlap pin the chunking options.
	ChunkSize    int `json:"chunk_size"`
	ChunkOverlap int `json:"chunk_overlap"`
	// KashVersion records the binary that compiled this corpus. Chunk metadata
	// changes across releases, so knowing the builder is what lets a later run
	// report an incompatibility rather than behave oddly.
	KashVersion string `json:"kash_version,omitempty"`
	// DomainSignature pins the structural rules baked into chunk metadata —
	// reference patterns and diacritic folding. A change makes stored metadata
	// inconsistent with the query path, so it requires a rebuild.
	DomainSignature string `json:"domain_signature,omitempty"`
	// PredicateSignature pins the extraction vocabulary. A change leaves
	// existing triples on the old vocabulary: degraded, not corrupt, so it is
	// reported rather than enforced.
	PredicateSignature string `json:"predicate_signature,omitempty"`
	// ChunkerRulesVersion pins how reference patterns were applied, which the
	// domain signature cannot cover: that hashes the pattern strings, so a
	// change to the way those strings are compiled leaves it identical. An
	// older value means references were tagged under superseded rules — fewer
	// of them found, not wrong ones — so it is reported rather than enforced.
	ChunkerRulesVersion int `json:"chunker_rules_version,omitempty"`
	// Documents maps source document name to its build state.
	Documents map[string]*DocState `json:"documents"`
}

// New returns an empty manifest at version 0 (no successful build yet).
func New() *Manifest {
	return &Manifest{
		Documents: map[string]*DocState{},
	}
}

// LoadOrNew reads a manifest from path, returning a fresh manifest when the
// file does not exist.
func LoadOrNew(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manifest %q: %w", path, err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %q: %w", path, err)
	}
	if m.Documents == nil {
		m.Documents = map[string]*DocState{}
	}
	return &m, nil
}

// Save writes the manifest atomically (temp file + rename) so an interrupted
// build never leaves a corrupt manifest behind.
func (m *Manifest) Save(path string) error {
	m.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	if err := fsutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	return nil
}

// HashContent returns the hex-encoded SHA-256 of a document's content.
func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
