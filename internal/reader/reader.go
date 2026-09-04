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

// Build artifacts that kash writes into the same directory it reads documents
// from. Listed literally rather than imported from their owning packages
// (manifest, lexical, graph, profile) because reader sits below all of them and
// importing upward would cycle. Keep in sync if a filename changes.
var (
	buildArtifactDirs = map[string]bool{
		"memory.chromem":   true,
		"knowledge.cayley": true,
	}
	buildArtifactFiles = map[string]bool{
		"build.manifest.json": true,
		"entity_aliases.json": true,
		"lexical.idx":         true,
		"domain.profile.json": true,
	}
)

func isBuildArtifactDir(name string) bool { return buildArtifactDirs[name] }

func isBuildArtifact(name string) bool {
	if buildArtifactFiles[name] {
		return true
	}
	// Temp files from the atomic temp+rename writers used by manifest, alias,
	// lexical and profile.
	return strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp")
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
			if isBuildArtifactDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		// Kash writes its own outputs into data/. Without this they are walked
		// as if they were source documents, so every build reported the
		// manifest and one line per embedded-store file as "not indexed".
		if isBuildArtifact(d.Name()) {
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
