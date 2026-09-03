package reader

import (
	"errors"
	"fmt"
	"io/fs"
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

// Rejection records a file that was found but could not be indexed, so the
// caller can report it rather than let the document vanish from the corpus.
type Rejection struct {
	// Path is the file that was rejected.
	Path string
	// Reason explains why, in terms a user can act on.
	Reason string
}

// LoadDirectory reads all supported documents from a directory tree.
//
// It recurses: documents in subdirectories used to be skipped entirely, which
// silently shrank the corpus. Files that are found but unusable are returned as
// rejections instead of being dropped, because a document that disappears
// without a trace is indistinguishable from one that was never added.
func LoadDirectory(dir string) ([]Document, []Rejection, error) {
	var (
		docs     []Document
		rejected []Rejection
	)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".md", ".txt", ".markdown":
			doc, err := loadTextFile(path)
			if err != nil {
				return fmt.Errorf("load text file %q: %w", path, err)
			}
			docs = append(docs, doc)

		case ".pdf":
			doc, err := loadPDF(path)
			if err != nil {
				rejected = append(rejected, Rejection{Path: path, Reason: err.Error()})
				return nil
			}
			docs = append(docs, doc)

		default:
			rejected = append(rejected, Rejection{
				Path:   path,
				Reason: "unsupported format (supported: .md, .markdown, .txt, .pdf)",
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("walk directory %q: %w", dir, err)
	}

	return docs, rejected, nil
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
