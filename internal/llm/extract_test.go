package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/config"
)

// captureExtraction runs ExtractTriples against a stub and returns the system
// and user messages the model would have seen.
func captureExtraction(t *testing.T, passages []string) (system, user string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openai.ChatCompletionRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		for _, m := range req.Messages {
			switch m.Role {
			case openai.ChatMessageRoleSystem:
				system = m.Content
			case openai.ChatMessageRoleUser:
				user = m.Content
			}
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Role: "assistant", Content: "[]"}},
			},
		})
	}))
	defer ts.Close()

	c, err := NewClient(&config.ProviderConfig{BaseURL: ts.URL, APIKey: "k", Model: "m"})
	require.NoError(t, err)

	_, err = c.ExtractTriples(context.Background(), passages, ExtractionSpec{
		Predicates: []string{"contains"},
	})
	require.NoError(t, err)
	return system, user
}

// The budget is per passage, not per request. A flat total silently rationed
// dense chunks: ten passages sharing a ceiling of 20 capped the graph at two
// facts per chunk however much a chunk actually stated.
func TestExtractionBudgetIsPerPassage(t *testing.T) {
	system, _ := captureExtraction(t, []string{"a", "b", "c"})

	assert.Contains(t, system, "per passage")
	assert.NotContains(t, system, "Extract 5-20 triples",
		"the old flat per-request ceiling must be gone")
}

// The prompt tells the model to read passage markers and to report a 1-based
// passage index. If the code that writes them and the prompt that describes
// them disagree, every triple's provenance is wrong and nothing reports it.
func TestJoinPassagesMatchesWhatThePromptDescribes(t *testing.T) {
	_, user := captureExtraction(t, []string{"first excerpt", "second excerpt"})

	assert.Contains(t, user, "--- PASSAGE 1 ---")
	assert.Contains(t, user, "--- PASSAGE 2 ---")
	assert.Contains(t, user, "first excerpt")
	assert.Contains(t, user, "second excerpt")
	assert.Less(t, strings.Index(user, "first excerpt"), strings.Index(user, "--- PASSAGE 2 ---"),
		"passage 1's text must sit under passage 1's marker")
}

func TestJoinPassagesNumbersFromOne(t *testing.T) {
	got := JoinPassages([]string{"x", "y"})
	assert.Equal(t, "--- PASSAGE 1 ---\nx\n\n--- PASSAGE 2 ---\ny\n\n", got)
}

func TestExtractTriplesRejectsAnEmptyVocabulary(t *testing.T) {
	c, err := NewClient(&config.ProviderConfig{BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"})
	require.NoError(t, err)

	_, err = c.ExtractTriples(context.Background(), []string{"a"}, ExtractionSpec{})
	require.Error(t, err, "an empty closed vocabulary would drop every fact")
}

// No passages means no request at all, rather than a prompt with an empty body.
func TestExtractTriplesWithNoPassagesMakesNoCall(t *testing.T) {
	c, err := NewClient(&config.ProviderConfig{BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"})
	require.NoError(t, err)

	triples, err := c.ExtractTriples(context.Background(), nil, ExtractionSpec{
		Predicates: []string{"contains"},
	})
	require.NoError(t, err)
	assert.Empty(t, triples)
}
