package reader

import (
	"fmt"
	"os"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// extractPDFText extracts plain text from a PDF file using pdfcpu.
func extractPDFText(path string) (string, error) {
	// Create a temp dir for extraction output
	tmpDir, err := os.MkdirTemp("", "agent-forge-pdf-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed

	// Extract text content pages
	if err := api.ExtractContentFile(path, tmpDir, nil, conf); err != nil {
		return "", fmt.Errorf("extract PDF content: %w", err)
	}

	// Read all extracted text files
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", fmt.Errorf("read temp dir: %w", err)
	}

	var sb strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(tmpDir + "/" + entry.Name())
		if err != nil {
			continue
		}
		sb.Write(data)
		sb.WriteString("\n")
	}

	text := sb.String()
	if text == "" {
		return "", fmt.Errorf("no text extracted from PDF")
	}
	return text, nil
}
