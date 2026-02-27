package reader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsupportedFormat is returned when a file format is not supported.
var ErrUnsupportedFormat = errors.New("unsupported file format")

// Document represents a loaded document.
type Document struct {
	// Path is the source file path
	Path string
	// Name is the base filename
	Name string
	// Content is the extracted text content
	Content string
}

// LoadDirectory reads all supported documents from a directory.
func LoadDirectory(dir string) ([]Document, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %q: %w", dir, err)
	}

	var docs []Document
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		ext := strings.ToLower(filepath.Ext(entry.Name()))

		switch ext {
		case ".md", ".txt", ".markdown":
			doc, err := loadTextFile(path)
			if err != nil {
				return nil, fmt.Errorf("load text file %q: %w", path, err)
			}
			docs = append(docs, doc)

		case ".pdf":
			doc, err := loadPDF(path)
			if err != nil {
				// Log and skip PDFs that can't be read
				fmt.Fprintf(os.Stderr, "warning: skipping PDF %q: %v\n", path, err)
				continue
			}
			docs = append(docs, doc)

		default:
			// Skip unsupported formats silently
			continue
		}
	}
	return docs, nil
}

// LoadFile reads a single document from the given path.
func LoadFile(path string) (Document, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".txt", ".markdown":
		return loadTextFile(path)
	case ".pdf":
		return loadPDF(path)
	default:
		return Document{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
}

func loadTextFile(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, fmt.Errorf("read file %q: %w", path, err)
	}
	return Document{
		Path:    path,
		Name:    filepath.Base(path),
		Content: string(data),
	}, nil
}

func loadPDF(path string) (Document, error) {
	// PDF extraction requires ledongthuc/pdfcpu or similar.
	// We use a lightweight approach with pdfcpu's text extraction.
	content, err := extractPDFText(path)
	if err != nil {
		return Document{}, fmt.Errorf("extract PDF text from %q: %w", path, err)
	}
	return Document{
		Path:    path,
		Name:    filepath.Base(path),
		Content: content,
	}, nil
}
