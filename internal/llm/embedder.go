package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/agent-forge/agent-forge/internal/config"
)

// ErrNilEmbedConfig is returned when nil embed config is provided.
var ErrNilEmbedConfig = errors.New("embedder config is nil")

// Embedder generates vector embeddings via an OpenAI-compatible API.
type Embedder struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	client     *http.Client
}

// NewEmbedder creates a new Embedder from a ProviderConfig.
func NewEmbedder(cfg *config.ProviderConfig) (*Embedder, error) {
	if cfg == nil {
		return nil, ErrNilEmbedConfig
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("embedder base_url is required")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("embedder api_key is required")
	}
	// Model is optional — embedding routers don't need it
	return &Embedder{
		baseURL:    strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		client:     &http.Client{},
	}, nil
}

type embedRequest struct {
	Input      []string `json:"input"`
	Model      string   `json:"model,omitempty"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// EmbedBatch generates embeddings for a batch of strings.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}

	embedReq := embedRequest{Input: texts}
	if e.model != "" {
		embedReq.Model = e.model
	}
	if e.dimensions > 0 {
		embedReq.Dimensions = e.dimensions
	}

	reqBody, err := json.Marshal(embedReq)
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	url := e.baseURL + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embed response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed API returned status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp embedResponse
	if err := json.Unmarshal(body, &embedResp); err != nil {
		return nil, fmt.Errorf("unmarshal embed response: %w", err)
	}

	if embedResp.Error != nil {
		return nil, fmt.Errorf("embed API error: %s", embedResp.Error.Message)
	}

	// Sort by index and extract embeddings
	result := make([][]float32, len(texts))
	for _, d := range embedResp.Data {
		if d.Index < len(result) {
			result[d.Index] = d.Embedding
		}
	}
	return result, nil
}

// Embed generates an embedding for a single string.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	results, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 || results[0] == nil {
		return nil, errors.New("embedder returned no embedding")
	}
	return results[0], nil
}

// Model returns the configured embedding model name.
func (e *Embedder) Model() string {
	return e.model
}
