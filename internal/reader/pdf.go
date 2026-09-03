package reader

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// extractPDFText extracts plain text from a PDF file.
// It uses ledongthuc/pdf which decodes font-encoded glyphs into valid UTF-8.
func extractPDFText(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", fmt.Errorf("open PDF: %w", err)
	}
	defer f.Close()

	reader, err := r.GetPlainText()
	if err != nil {
		return "", fmt.Errorf("extract PDF text: %w", err)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return "", fmt.Errorf("read PDF text: %w", err)
	}

	text := buf.String()
	if text == "" {
		return "", fmt.Errorf("no text extracted from PDF")
	}

	// Emptiness is not a sufficient check. A PDF whose embedded font subsets
	// carry no usable ToUnicode CMap yields glyph indices rendered as arbitrary
	// letters — valid UTF-8, non-empty, and a substitution cipher. Indexing it
	// costs a full pass of embedding API calls and produces chunks no query can
	// ever retrieve, with nothing anywhere reporting a problem.
	if q := AssessText(text); q.Suspect {
		return "", fmt.Errorf("extracted text does not look like language: %s", q.Reason)
	}

	// Sanitize: replace any remaining invalid UTF-8 sequences with the
	// Unicode replacement character so downstream processing never fails.
	if !utf8.ValidString(text) {
		var sb strings.Builder
		sb.Grow(len(text))
		for _, r := range text {
			sb.WriteRune(r)
		}
		text = sb.String()
	}

	return text, nil
}
