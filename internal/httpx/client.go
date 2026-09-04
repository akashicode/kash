// Package httpx supplies the HTTP clients Kash uses to reach model providers.
package httpx

import (
	"net"
	"net/http"
	"time"
)

// Default response-header budgets, chosen from what each call actually costs.
//
// They are ceilings on a stalled request, not targets. A call that reaches one
// has already gone wrong; the point is that it fails and gets retried instead
// of blocking the process.
const (
	// EmbedTimeout bounds one embedding request. These return in a second or
	// two, but a build makes tens of thousands of them sequentially, so the
	// ceiling is set well clear of a slow provider rather than close to typical.
	EmbedTimeout = 2 * time.Minute

	// RerankTimeout bounds one rerank request. It sits inside a user's query,
	// so waiting minutes for it helps nobody.
	RerankTimeout = 45 * time.Second

	// ChatTimeout bounds one chat completion with reasoning disabled.
	// Bounded so a stalled provider fails and gets retried, but with enough
	// headroom (4m) for dense 10-chunk knowledge-graph extraction passes.
	ChatTimeout = 4 * time.Minute
)

// ChatTimeoutFor returns the response-header budget for a chat completion at a
// given reasoning effort.
//
// Reasoning effort is a dial on how long the model thinks before it answers,
// which is exactly what this timeout has to accommodate, so it scales with it
// rather than forcing one ceiling to cover both a plain completion and a
// high-effort reasoning call on a long prompt.
func ChatTimeoutFor(reasoningEffort string) time.Duration {
	switch reasoningEffort {
	case "low":
		return 5 * time.Minute
	case "medium":
		return 8 * time.Minute
	case "high":
		return 12 * time.Minute
	default:
		return ChatTimeout
	}
}

// Provider returns an HTTP client for calls to a model provider, bounded so a
// stalled request cannot block forever.
//
// The standard library's zero-value http.Client applies no deadline once a
// request is on the wire. DefaultTransport bounds dialling and the TLS
// handshake, but nothing bounds the wait for a response, so a provider that
// accepts a request and then stalls — or an intermediary that drops the
// connection without sending a RST — leaves the caller blocked indefinitely.
// TCP keep-alives do not rescue this: a NAT that has forgotten the flow drops
// the probes too, and the OS gives up on its own schedule, typically after
// hours.
//
// That is not a hypothetical here. A build issues tens of thousands of
// sequential provider calls, and one that never returns hangs the whole build
// with no error, no output and no way to tell it apart from slow progress.
//
// The bound is ResponseHeaderTimeout rather than http.Client.Timeout because
// Timeout also covers reading the response body, which would sever a streaming
// chat completion mid-answer. Waiting for the first byte of the response is the
// phase that actually hangs, and it is the only one bounded here.
func Provider(headerTimeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   runtimeIdleConns,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: headerTimeout,
		},
	}
}

// runtimeIdleConns keeps connections to the one provider host warm. The default
// of 2 makes parallel embedding reconnect for almost every request.
const runtimeIdleConns = 32
