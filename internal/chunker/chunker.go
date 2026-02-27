package chunker

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrInvalidChunkSize is returned when an invalid chunk size is specified.
var ErrInvalidChunkSize = errors.New("chunk size must be greater than 0")

// ErrNilInput is returned when a nil source is provided.
var ErrNilInput = errors.New("input source is nil")

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

// OptionsFromMaxTokens computes chunk options from a model's token limit.
// It uses a conservative estimate of ~4 characters per token and applies a
// 90% safety margin so chunks stay well under the model's maximum.
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

// ChunkDocument is a convenience function for chunking with default options.
func ChunkDocument(text string, chunkSize int) ([]Chunk, error) {
	if chunkSize <= 0 {
		return nil, ErrInvalidChunkSize
	}
	c, err := NewChunker(Options{ChunkSize: chunkSize, Overlap: chunkSize / 5})
	if err != nil {
		return nil, err
	}
	return c.ChunkText(text, "")
}

// SplitBySentence splits text into sentence-aware chunks, attempting to break
// at sentence boundaries when possible. Oversized paragraphs are sub-split
// at sentence boundaries; truly huge sentences fall back to character-level
// splitting via ChunkText.
func (c *Chunker) SplitBySentence(text, source string) ([]Chunk, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("text is not valid UTF-8")
	}

	// Normalize line endings (\r\n → \n) so paragraph splitting works on all platforms
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// Split into paragraphs first
	paragraphs := strings.Split(text, "\n\n")

	var builder strings.Builder
	chunks := []Chunk{}
	idx := 0

	flush := func() {
		content := strings.TrimSpace(builder.String())
		if content != "" {
			chunks = append(chunks, Chunk{
				ID:      buildChunkID(source, idx),
				Content: content,
				Source:  source,
				Index:   idx,
			})
			idx++
		}
		builder.Reset()
	}

	// addFragment adds a piece of text that is guaranteed to be <= ChunkSize.
	addFragment := func(frag string) {
		frag = strings.TrimSpace(frag)
		if frag == "" {
			return
		}
		if builder.Len()+len(frag)+2 > c.opts.ChunkSize && builder.Len() > 0 {
			flush()
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(frag)
	}

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// If the paragraph fits, accumulate it normally
		if len(para) <= c.opts.ChunkSize {
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

			if len(sent) <= c.opts.ChunkSize {
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

// splitSentences splits text at sentence boundaries (. ! ?) followed by a space
// or end of string. It keeps the delimiter attached to the preceding sentence.
func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		current.WriteRune(runes[i])

		// Check for sentence-ending punctuation
		if runes[i] == '.' || runes[i] == '!' || runes[i] == '?' {
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
		return "chunk_" + itoa(idx)
	}
	// Sanitize source for use as an ID prefix
	sanitized := strings.ReplaceAll(source, "/", "_")
	sanitized = strings.ReplaceAll(sanitized, "\\", "_")
	sanitized = strings.ReplaceAll(sanitized, ".", "_")
	sanitized = strings.ReplaceAll(sanitized, " ", "_")
	return sanitized + "_" + itoa(idx)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
