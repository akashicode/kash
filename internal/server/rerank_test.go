package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentconfig "github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/llm"
	"github.com/akashicode/kash/internal/vector"
)

// rerankStub stands in for a Cohere-compatible provider. It reverses the
// candidates it is given and records how many it was sent.
func rerankStub(t *testing.T, sent *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Documents []string `json:"documents"`
		}
		require.NoError(t, json.Unmarshal(body, &req))
		*sent = len(req.Documents)

		type result struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		}
		out := make([]result, len(req.Documents))
		for i := range req.Documents {
			// Reverse: the last candidate becomes the most relevant.
			out[i] = result{Index: len(req.Documents) - 1 - i, RelevanceScore: float64(len(req.Documents) - i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
	}))
}

func newRerankTestServer(t *testing.T, endpoint string) *Server {
	t.Helper()
	rr, err := llm.NewReranker(&agentconfig.ProviderConfig{
		BaseURL: endpoint,
		APIKey:  "k",
		Model:   "rerank-test",
	})
	require.NoError(t, err)
	require.NotNil(t, rr)
	return &Server{reranker: rr, log: slog.New(slog.DiscardHandler)}
}

func candidates(n int) []vector.SearchResult {
	out := make([]vector.SearchResult, n)
	for i := range out {
		out[i] = vector.SearchResult{ID: fmt.Sprintf("c%d", i), Content: fmt.Sprintf("chunk %d", i)}
	}
	return out
}

// The candidate pool is sized for fusion and reaches 2000 at a high top_k.
// Sending all of it to a paid rerank endpoint makes a request providers cap or
// bill by the hundred, and a rejected rerank degrades silently to cosine order.
func TestRerankBoundsWhatItSendsToTheProvider(t *testing.T) {
	var sent int
	ts := rerankStub(t, &sent)
	defer ts.Close()

	s := newRerankTestServer(t, ts.URL)
	in := candidates(500)
	out := s.rerank(context.Background(), "q", in)

	assert.Equal(t, maxRerankCandidates, sent, "only the head of the pool is sent")
	assert.Len(t, out, len(in), "nothing is dropped — the tail is carried, not discarded")
}

// The tail keeps its similarity order behind the reranked head, so fusion still
// sees a complete ranking of the vector route.
func TestRerankCarriesTheTailInOrder(t *testing.T) {
	var sent int
	ts := rerankStub(t, &sent)
	defer ts.Close()

	s := newRerankTestServer(t, ts.URL)
	in := candidates(maxRerankCandidates + 3)
	out := s.rerank(context.Background(), "q", in)

	require.Len(t, out, len(in))
	// Stub reverses the head, so candidate 99 leads.
	assert.Equal(t, "c99", out[0].ID)
	// The tail follows, undisturbed.
	assert.Equal(t, "c100", out[maxRerankCandidates].ID)
	assert.Equal(t, "c101", out[maxRerankCandidates+1].ID)
	assert.Equal(t, "c102", out[maxRerankCandidates+2].ID)
}

// Below the cap nothing is held back.
func TestRerankSendsEverythingBelowTheCap(t *testing.T) {
	var sent int
	ts := rerankStub(t, &sent)
	defer ts.Close()

	s := newRerankTestServer(t, ts.URL)
	out := s.rerank(context.Background(), "q", candidates(30))

	assert.Equal(t, 30, sent)
	require.Len(t, out, 30)
	assert.Equal(t, "c29", out[0].ID)
}
