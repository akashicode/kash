package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/config"
)

func TestClientReasoningEffort(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		var capturedReq map[string]any
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&capturedReq)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
				Choices: []openai.ChatCompletionChoice{
					{Message: openai.ChatCompletionMessage{Role: "assistant", Content: "ok"}},
				},
			})
		}))
		defer ts.Close()

		cfg := &config.ProviderConfig{
			BaseURL: ts.URL,
			APIKey:  "test-key",
			Model:   "gpt-4o",
		}

		client, err := NewClient(cfg)
		require.NoError(t, err)
		assert.Equal(t, "", client.ReasoningEffort())

		resp, err := client.Complete(context.Background(), "system", "hello")
		require.NoError(t, err)
		assert.Equal(t, "ok", resp)

		// reasoning_effort should be omitted from JSON payload
		_, hasReasoning := capturedReq["reasoning_effort"]
		assert.False(t, hasReasoning, "reasoning_effort should be omitted when disabled")
	})

	t.Run("configured with low reasoning effort", func(t *testing.T) {
		var capturedReq map[string]any
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&capturedReq)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
				Choices: []openai.ChatCompletionChoice{
					{Message: openai.ChatCompletionMessage{Role: "assistant", Content: "reasoned response"}},
				},
			})
		}))
		defer ts.Close()

		cfg := &config.ProviderConfig{
			BaseURL:         ts.URL,
			APIKey:          "test-key",
			Model:           "o3-mini",
			ReasoningEffort: "low",
		}

		client, err := NewClient(cfg)
		require.NoError(t, err)
		assert.Equal(t, "low", client.ReasoningEffort())

		resp, err := client.Complete(context.Background(), "system", "hello")
		require.NoError(t, err)
		assert.Equal(t, "reasoned response", resp)

		// reasoning_effort should be present in JSON payload
		val, hasReasoning := capturedReq["reasoning_effort"]
		assert.True(t, hasReasoning, "reasoning_effort should be present")
		assert.Equal(t, "low", val)
	})

	t.Run("ChatWithContext per-request override", func(t *testing.T) {
		var capturedReq map[string]any
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&capturedReq)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
				Choices: []openai.ChatCompletionChoice{
					{Message: openai.ChatCompletionMessage{Role: "assistant", Content: "high reasoning answer"}},
				},
			})
		}))
		defer ts.Close()

		cfg := &config.ProviderConfig{
			BaseURL:         ts.URL,
			APIKey:          "test-key",
			Model:           "o3-mini",
			ReasoningEffort: "low",
		}

		client, err := NewClient(cfg)
		require.NoError(t, err)

		// Override with "high"
		resp, err := client.ChatWithContext(context.Background(), []openai.ChatCompletionMessage{
			{Role: "user", Content: "solve problem"},
		}, "ctx", "high")
		require.NoError(t, err)
		assert.Equal(t, "high reasoning answer", resp)

		val, hasReasoning := capturedReq["reasoning_effort"]
		assert.True(t, hasReasoning)
		assert.Equal(t, "high", val)
	})
}
