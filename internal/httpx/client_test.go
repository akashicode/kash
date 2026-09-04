package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The failure this guards against is a hang, not an error: a provider that
// accepts the request and then never responds. A zero-value http.Client waits
// for it forever, which during a build means no output, no error and no way to
// tell a dead call from a slow one.
func TestProviderFailsOnAStalledResponse(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // never write a header until the test says so
	}))
	defer ts.Close()
	defer close(release)

	client := Provider(150 * time.Millisecond)

	start := time.Now()
	_, err := client.Get(ts.URL)

	require.Error(t, err, "a stalled provider must fail, not block")
	assert.Less(t, time.Since(start), 5*time.Second, "must give up near the configured budget")
}

func TestProviderAllowsASlowBodyAfterHeaders(t *testing.T) {
	// ResponseHeaderTimeout is deliberately not http.Client.Timeout: a stream
	// that has started sending must be allowed to keep going past the budget,
	// or a long chat completion would be severed mid-answer.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer ts.Close()

	client := Provider(100 * time.Millisecond)

	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	assert.Equal(t, "late", string(body))
}

func TestChatTimeoutScalesWithReasoningEffort(t *testing.T) {
	// Reasoning effort is a dial on how long the model thinks, so the budget
	// that has to accommodate it moves with it.
	assert.Equal(t, ChatTimeout, ChatTimeoutFor(""))
	assert.Equal(t, ChatTimeout, ChatTimeoutFor("nonsense"))
	assert.Greater(t, ChatTimeoutFor("low"), ChatTimeoutFor(""))
	assert.Greater(t, ChatTimeoutFor("medium"), ChatTimeoutFor("low"))
	assert.Greater(t, ChatTimeoutFor("high"), ChatTimeoutFor("medium"))
}

func TestProviderSetsTheHeaderBudget(t *testing.T) {
	c := Provider(42 * time.Second)
	tr, ok := c.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 42*time.Second, tr.ResponseHeaderTimeout)
	assert.Zero(t, c.Timeout, "a whole-request deadline would cut off streaming")
}
