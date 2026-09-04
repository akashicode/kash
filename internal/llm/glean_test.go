package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akashicode/kash/internal/config"
)

// stubSequence creates a stub that returns each response in order and counts calls.
// If more calls arrive than responses, it returns the last entry.
func stubSequence(t *testing.T, responses []string) (url string, calls *int32) {
	t.Helper()
	var n int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := int(atomic.AddInt32(&n, 1)) - 1
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		_ = json.NewEncoder(w).Encode(openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{
				{Message: openai.ChatCompletionMessage{Role: "assistant", Content: responses[idx]}},
			},
		})
	}))
	t.Cleanup(ts.Close)
	return ts.URL, &n
}

func baseSpec(rounds int) ExtractionSpec {
	return ExtractionSpec{
		Predicates:  []string{"contains", "created by", "is part of"},
		GleanRounds: rounds,
	}
}

// TestGleanRoundsZeroMakesOneCall verifies that GleanRounds=0 disables gleaning
// and results in exactly one LLM call (the initial extraction pass).
func TestGleanRoundsZeroMakesOneCall(t *testing.T) {
	initial := `[{"subject":"Tantra","predicate":"contains","object":"108 verses","passage":1}]`
	url, calls := stubSequence(t, []string{initial})

	c, err := NewClient(&config.ProviderConfig{BaseURL: url, APIKey: "k", Model: "m"})
	require.NoError(t, err)

	triples, err := c.ExtractTriples(context.Background(), []string{"Tantra contains 108 verses."}, baseSpec(0))
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(calls), "exactly one call must be made when GleanRounds=0")
	require.Len(t, triples, 1)
	assert.Equal(t, "Tantra", triples[0].Subject)
}

// TestGleanRoundsOneRecoversMissedTriples verifies that a gleaning round adds
// triples the initial pass missed, without duplicating the ones already found.
func TestGleanRoundsOneRecoversMissedTriples(t *testing.T) {
	initial := `[{"subject":"Abhinavagupta","predicate":"created by","object":"Tantraloka","passage":1}]`
	glean := `[{"subject":"Tantraloka","predicate":"contains","object":"37 chapters","passage":1}]`

	url, calls := stubSequence(t, []string{initial, glean})

	c, err := NewClient(&config.ProviderConfig{BaseURL: url, APIKey: "k", Model: "m"})
	require.NoError(t, err)

	triples, err := c.ExtractTriples(context.Background(), []string{"Abhinavagupta created the Tantraloka, which contains 37 chapters."}, baseSpec(1))
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(calls), "one initial + one gleaning call expected")
	require.Len(t, triples, 2)

	subjects := map[string]bool{}
	for _, tr := range triples {
		subjects[tr.Subject] = true
	}
	assert.True(t, subjects["Abhinavagupta"])
	assert.True(t, subjects["Tantraloka"])
}

// TestGleanEarlyExitOnEmptyResponse verifies that when the gleaning pass returns []
// the loop stops immediately and no further calls are made.
func TestGleanEarlyExitOnEmptyResponse(t *testing.T) {
	initial := `[{"subject":"Shiva","predicate":"is part of","object":"Shaiva tradition","passage":1}]`
	emptyGlean := `[]`

	url, calls := stubSequence(t, []string{initial, emptyGlean})

	c, err := NewClient(&config.ProviderConfig{BaseURL: url, APIKey: "k", Model: "m"})
	require.NoError(t, err)

	// Two rounds configured, but the first glean returns [] so the second should not fire.
	triples, err := c.ExtractTriples(context.Background(), []string{"Shiva is part of the Shaiva tradition."}, baseSpec(2))
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(calls), "must stop after empty gleaning response even with rounds=2")
	require.Len(t, triples, 1)
}

// TestGleanDeduplicatesRediscoveredTriples verifies that a triple the model returns
// again on a gleaning round is silently dropped and not appended twice.
func TestGleanDeduplicatesRediscoveredTriples(t *testing.T) {
	initial := `[{"subject":"Kali","predicate":"is part of","object":"Shakta tradition","passage":1}]`
	// Gleaning response re-emits the same triple with slightly different casing.
	duplicate := `[{"subject":"kali","predicate":"is part of","object":"Shakta tradition","passage":1}]`

	url, calls := stubSequence(t, []string{initial, duplicate})

	c, err := NewClient(&config.ProviderConfig{BaseURL: url, APIKey: "k", Model: "m"})
	require.NoError(t, err)

	triples, err := c.ExtractTriples(context.Background(), []string{"Kali is part of the Shakta tradition."}, baseSpec(1))
	require.NoError(t, err)
	assert.Equal(t, int32(2), atomic.LoadInt32(calls))
	// Only one triple -- the duplicate must have been dropped.
	require.Len(t, triples, 1, "duplicate gleaned triple must not be appended")
}

// TestGleanRoundsTwoCollectsFromBothRounds verifies that when two gleaning rounds
// are configured and each returns new triples, all are merged into the result.
func TestGleanRoundsTwoCollectsFromBothRounds(t *testing.T) {
	initial := `[{"subject":"A","predicate":"contains","object":"B","passage":1}]`
	round1 := `[{"subject":"B","predicate":"contains","object":"C","passage":1}]`
	round2 := `[{"subject":"C","predicate":"is part of","object":"D","passage":1}]`

	url, calls := stubSequence(t, []string{initial, round1, round2})

	c, err := NewClient(&config.ProviderConfig{BaseURL: url, APIKey: "k", Model: "m"})
	require.NoError(t, err)

	triples, err := c.ExtractTriples(context.Background(), []string{"A contains B contains C is part of D."}, baseSpec(2))
	require.NoError(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(calls), "initial + 2 gleaning calls expected")
	require.Len(t, triples, 3)
}

// TestGleanContinuationPromptStructure verifies that the gleaning call sends a
// 4-message conversation: system, user, assistant echo, gleaning-user prompt.
func TestGleanContinuationPromptStructure(t *testing.T) {
	var capturedMessages []openai.ChatCompletionMessage
	var callCount int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openai.ChatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		callCount++
		if callCount == 2 {
			capturedMessages = req.Messages
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

	_, err = c.ExtractTriples(context.Background(), []string{"test passage"}, baseSpec(1))
	require.NoError(t, err)

	// The second call (gleaning) must carry 4 messages.
	require.Len(t, capturedMessages, 4, "gleaning call must carry system+user+assistant+glean-prompt")
	assert.Equal(t, openai.ChatMessageRoleSystem, capturedMessages[0].Role)
	assert.Equal(t, openai.ChatMessageRoleUser, capturedMessages[1].Role)
	assert.Equal(t, openai.ChatMessageRoleAssistant, capturedMessages[2].Role)
	assert.Equal(t, openai.ChatMessageRoleUser, capturedMessages[3].Role)

	// The continuation prompt must ask for missed facts only.
	assert.Contains(t, capturedMessages[3].Content, "EXPLICITLY STATED")
	assert.Contains(t, capturedMessages[3].Content, "nothing new, return []")
}
