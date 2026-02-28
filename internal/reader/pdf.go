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
