package chunker

import (
	"errors"
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
// at sentence boundaries when possible.
func (c *Chunker) SplitBySentence(text, source string) ([]Chunk, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("text is not valid UTF-8")
	}

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

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if builder.Len()+len(para) > c.opts.ChunkSize {
			flush()
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(para)
	}
	flush()

	return chunks, nil
}

func buildChunkID(source string, idx int) string {
	if source == "" {
		return strings.ReplaceAll(
			strings.ToLower(strings.ReplaceAll(source, " ", "_")),
			"/", "_",
		) + "_" + itoa(idx)
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
