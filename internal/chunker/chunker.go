package chunker

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrInvalidChunkSize is returned when an invalid chunk size is specified.
var ErrInvalidChunkSize = errors.New("chunk size must be greater than 0")

// Chunk represents a single chunk of text from a document.
type Chunk struct {
	// ID is a unique identifier for the chunk (e.g., "source_file_0")
	ID string
	// Content is the chunk text
	Content string
	// Source is the originating file path
	Source string
	// Index is the position of this chunk within the source
	Index int
	// Metadata carries structural facts about the chunk — book, heading
	// breadcrumb, verse/dhāraṇā number, content type and noise score. Retrieval
	// filters and ranks on these; see the Meta* constants in structure.go.
	Metadata map[string]string
}

// Options configures the chunking behavior.
type Options struct {
	// ChunkSize is the maximum number of characters per chunk
	ChunkSize int
	// Overlap is the number of characters to overlap between chunks
	Overlap int
}

// DefaultOptions returns sensible defaults for chunking.
func DefaultOptions() Options {
	return Options{
		ChunkSize: 1000,
		Overlap:   200,
	}
}

// MaxRetrievalChunkSize caps auto-tuned chunk size regardless of the
// embedder's token limit. The model's max tokens is a hard ceiling, not a
// target: oversized chunks (e.g. ~115K chars for a 32K-token model) blend
// many topics into one embedding and destroy retrieval precision.
// ~2000 chars (~500 tokens) keeps chunks topically focused. An explicit
// build.chunk_size in agent.yaml may exceed this (with a warning).
const MaxRetrievalChunkSize = 2000

// OptionsFromMaxTokens computes chunk options from a model's token limit.
// It uses a conservative estimate of ~4 characters per token and applies a
// 90% safety margin so chunks stay well under the model's maximum, then caps
// the result at MaxRetrievalChunkSize for retrieval quality.
// Returns DefaultOptions if maxTokens is <= 0.
func OptionsFromMaxTokens(maxTokens int) Options {
	if maxTokens <= 0 {
		return DefaultOptions()
	}
	// Conservative: ~4 chars/token, use 90% of limit
	chunkSize := int(float64(maxTokens) * 4 * 0.9)
	if chunkSize < 200 {
		chunkSize = 200 // absolute floor
	}
	if chunkSize > MaxRetrievalChunkSize {
		chunkSize = MaxRetrievalChunkSize
	}
	overlap := chunkSize / 5
	return Options{
		ChunkSize: chunkSize,
		Overlap:   overlap,
	}
}

// Chunker splits documents into overlapping text chunks.
type Chunker struct {
	opts Options
}

// NewChunker creates a new Chunker with the given options.
func NewChunker(opts Options) (*Chunker, error) {
	if opts.ChunkSize <= 0 {
		return nil, ErrInvalidChunkSize
	}
	if opts.Overlap < 0 {
		opts.Overlap = 0
	}
	if opts.Overlap >= opts.ChunkSize {
		opts.Overlap = opts.ChunkSize / 4
	}
	return &Chunker{opts: opts}, nil
}

// ChunkText splits a text string into overlapping chunks.
func (c *Chunker) ChunkText(text, source string) ([]Chunk, error) {
	if text == "" {
		return []Chunk{}, nil
	}

	// Normalize line endings
	text = strings.ReplaceAll(text, "\r\n", "\n")

	chunks := []Chunk{}
	runes := []rune(text)
	total := len(runes)
	step := c.opts.ChunkSize - c.opts.Overlap
	if step <= 0 {
		step = c.opts.ChunkSize
	}

	idx := 0
	for start := 0; start < total; start += step {
		end := start + c.opts.ChunkSize
		if end > total {
			end = total
		}

		content := string(runes[start:end])
		content = strings.TrimSpace(content)
		if content == "" {
			idx++
			continue
		}

		chunk := Chunk{
			ID:      buildChunkID(source, idx),
			Content: content,
			Source:  source,
			Index:   idx,
		}
		chunks = append(chunks, chunk)
		idx++

		if end == total {
			break
		}
	}

	return chunks, nil
}

// SplitBySentence splits text into sentence-aware chunks, attempting to break
// at sentence boundaries when possible. Oversized paragraphs are sub-split
// at sentence boundaries; truly huge sentences fall back to character-level
// splitting via ChunkText. Consecutive chunks share an overlap tail so
// context spanning a chunk boundary is retrievable from either side.
func (c *Chunker) SplitBySentence(text, source string) ([]Chunk, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("text is not valid UTF-8")
	}

	// Normalize line endings (\r\n → \n) so paragraph splitting works on all platforms
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// Split into paragraphs first
	paragraphs := strings.Split(text, "\n\n")

	var builder strings.Builder
	runeLen := 0      // rune length of builder content (builder.Len() is bytes)
	carryOnly := true // builder holds only overlap carry — never emit it alone
	chunks := []Chunk{}
	idx := 0

	flush := func() {
		content := strings.TrimSpace(builder.String())
		wasCarryOnly := carryOnly
		builder.Reset()
		runeLen = 0
		carryOnly = true
		if content == "" || wasCarryOnly {
			return
		}
		chunks = append(chunks, Chunk{
			ID:      buildChunkID(source, idx),
			Content: content,
			Source:  source,
			Index:   idx,
		})
		idx++

		// Seed the next chunk with an overlap tail from this one
		if tail := overlapTail(content, c.opts.Overlap); tail != "" {
			builder.WriteString(tail)
			runeLen = utf8.RuneCountInString(tail)
		}
	}

	// addFragment adds a piece of text that is guaranteed to be <= ChunkSize.
	addFragment := func(frag string) {
		frag = strings.TrimSpace(frag)
		if frag == "" {
			return
		}
		fragLen := utf8.RuneCountInString(frag)
		if runeLen > 0 && runeLen+fragLen+2 > c.opts.ChunkSize {
			flush()
		}
		if runeLen > 0 {
			builder.WriteString("\n\n")
			runeLen += 2
		}
		builder.WriteString(frag)
		runeLen += fragLen
		carryOnly = false
	}

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// If the paragraph fits, accumulate it normally
		if utf8.RuneCountInString(para) <= c.opts.ChunkSize {
			addFragment(para)
			continue
		}

		// Paragraph is oversized — flush any accumulated text first
		flush()

		// Try to sub-split at sentence boundaries
		sentences := splitSentences(para)
		for _, sent := range sentences {
			sent = strings.TrimSpace(sent)
			if sent == "" {
				continue
			}

			if utf8.RuneCountInString(sent) <= c.opts.ChunkSize {
				addFragment(sent)
				continue
			}

			// Single sentence still exceeds ChunkSize — fall back to
			// character-level splitting with overlap.
			flush()
			subChunks, err := c.ChunkText(sent, source)
			if err != nil {
				return nil, fmt.Errorf("sub-split oversized sentence: %w", err)
			}
			for _, sc := range subChunks {
				chunks = append(chunks, Chunk{
					ID:      buildChunkID(source, idx),
					Content: sc.Content,
					Source:  source,
					Index:   idx,
				})
				idx++
			}
		}
	}
	flush()

	return chunks, nil
}

// overlapTail returns the last ~overlap runes of content, snapped forward to
// a whitespace boundary so the carry starts on a whole word. Returns "" when
// the content is not longer than the overlap (carrying it all would just
// duplicate the chunk).
func overlapTail(content string, overlap int) string {
	if overlap <= 0 {
		return ""
	}
	runes := []rune(content)
	if len(runes) <= overlap {
		return ""
	}
	tail := string(runes[len(runes)-overlap:])
	if i := strings.IndexAny(tail, " \n\t"); i >= 0 {
		tail = tail[i:]
	}
	return strings.TrimSpace(tail)
}

// splitSentences splits text at sentence boundaries (. ! ? and the Devanagari
// danda । / double danda ॥) followed by a space or end of string. It keeps the
// delimiter attached to the preceding sentence.
func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		current.WriteRune(runes[i])

		// Check for sentence-ending punctuation
		if runes[i] == '.' || runes[i] == '!' || runes[i] == '?' || runes[i] == '।' || runes[i] == '॥' {
			// Consider it a sentence boundary if followed by a space, newline, or end of text
			if i+1 >= len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n' || runes[i+1] == '\t' {
				sentences = append(sentences, current.String())
				current.Reset()
			}
		}
	}

	// Remaining text (if any)
	if current.Len() > 0 {
		sentences = append(sentences, current.String())
	}

	return sentences
}

func buildChunkID(source string, idx int) string {
	if source == "" {
		return "chunk_" + strconv.Itoa(idx)
	}
	// Sanitize source for use as an ID prefix
	sanitized := strings.ReplaceAll(source, "/", "_")
	sanitized = strings.ReplaceAll(sanitized, "\\", "_")
	sanitized = strings.ReplaceAll(sanitized, ".", "_")
	sanitized = strings.ReplaceAll(sanitized, " ", "_")
	return sanitized + "_" + strconv.Itoa(idx)
}

