package display

import (
	"fmt"
	"os"
	"strings"
)

// ANSI color codes
const (
	reset   = "\033[0m"
	bold    = "\033[1m"
	dim     = "\033[2m"
	italic  = "\033[3m"

	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	white   = "\033[37m"

	brightRed     = "\033[91m"
	brightGreen   = "\033[92m"
	brightYellow  = "\033[93m"
	brightBlue    = "\033[94m"
	brightMagenta = "\033[95m"
	brightCyan    = "\033[96m"
	brightWhite   = "\033[97m"

	bgBlue    = "\033[44m"
	bgMagenta = "\033[45m"
	bgCyan    = "\033[46m"
)

// ServerInfo holds all the information to display in the startup banner.
type ServerInfo struct {
	// Agent info
	AgentName        string
	AgentDescription string
	AgentVersion     string

	// Data stats
	VectorCount int
	TripleCount int64
	MCPTools    int

	// Embedding
	EmbedDimensions int
	EmbedModel      string
	EmbedBaseURL    string

	// LLM
	LLMModel   string
	LLMBaseURL string

	// Reranker (optional)
	RerankModel   string
	RerankBaseURL string

	// Security
	AuthEnabled bool

	// Server
	Port int
}

// PrintBanner prints a fancy colorful startup banner with all server information.
func PrintBanner(info ServerInfo) {
	w := os.Stdout

	addr := fmt.Sprintf(":%d", info.Port)
	host := fmt.Sprintf("http://localhost%s", addr)

	// Header
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s%s⚡ Agent-Forge Runtime Server%s\n", bold, brightCyan, reset)
	fmt.Fprintf(w, "  %s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", dim, cyan, reset)
	fmt.Fprintln(w)

	// Agent Info section
	printSectionHeader(w, "🤖 Agent")
	printKV(w, "Name", info.AgentName, brightWhite)
	if info.AgentDescription != "" {
		desc := info.AgentDescription
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		printKV(w, "Description", desc, white)
	}
	if info.AgentVersion != "" {
		printKV(w, "Version", info.AgentVersion, white)
	}
	fmt.Fprintln(w)

	// Knowledge Base section
	printSectionHeader(w, "📚 Knowledge Base")
	printKVColored(w, "Vectors", formatCount(info.VectorCount), brightGreen)
	printKVColored(w, "Graph Triples", formatCount64(info.TripleCount), brightGreen)
	if info.MCPTools > 0 {
		printKVColored(w, "MCP Tools", fmt.Sprintf("%d", info.MCPTools), brightGreen)
	}
	printKVColored(w, "Embed Dimensions", fmt.Sprintf("%d", info.EmbedDimensions), brightYellow)
	fmt.Fprintln(w)

	// Runtime Config section
	printSectionHeader(w, "⚙️  Runtime Configuration")
	printKV(w, "LLM Model", info.LLMModel, brightMagenta)
	printKV(w, "LLM Endpoint", maskURL(info.LLMBaseURL), dim+white)
	if info.EmbedModel != "" {
		printKV(w, "Embed Model", info.EmbedModel, brightMagenta)
	} else {
		printKV(w, "Embed Model", "(router — no model specified)", dim+yellow)
	}
	printKV(w, "Embed Endpoint", maskURL(info.EmbedBaseURL), dim+white)
	if info.RerankBaseURL != "" {
		printKVColored(w, "Reranker", "✓ enabled", brightGreen)
		if info.RerankModel != "" {
			printKV(w, "Rerank Model", info.RerankModel, brightMagenta)
		}
		printKV(w, "Rerank Endpoint", maskURL(info.RerankBaseURL), dim+white)
	} else {
		printKVColored(w, "Reranker", "✗ disabled", dim+white)
	}
	if info.AuthEnabled {
		printKVColored(w, "Auth", "✓ API key required", brightGreen)
	} else {
		printKVColored(w, "Auth", "✗ open (set AGENT_API_KEY to enable)", brightYellow)
	}
	fmt.Fprintln(w)

	// Endpoints section
	printSectionHeader(w, "🌐 Endpoints")
	printEndpoint(w, "REST ", "POST", host+"/v1/chat/completions", brightBlue)
	printEndpoint(w, "MCP  ", "GET ", host+"/mcp", brightCyan)
	printEndpoint(w, "A2A  ", "POST", host+"/rpc/agent", brightMagenta)
	printEndpoint(w, "Health", "GET ", host+"/health", green)
	fmt.Fprintln(w)

	// Footer
	fmt.Fprintf(w, "  %s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", dim, cyan, reset)
	fmt.Fprintf(w, "  %s%s🚀 Server listening on %s%s%s%s\n", dim, white, reset, bold+brightGreen, host, reset)
	fmt.Fprintf(w, "  %s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", dim, cyan, reset)
	fmt.Fprintln(w)
}

func printSectionHeader(w *os.File, title string) {
	fmt.Fprintf(w, "  %s%s%s%s\n", bold, brightYellow, title, reset)
}

func printKV(w *os.File, key, value, valueColor string) {
	paddedKey := padRight(key, 18)
	fmt.Fprintf(w, "    %s%s%s  %s%s%s\n", dim, paddedKey, reset, valueColor, value, reset)
}

func printKVColored(w *os.File, key, value, valueColor string) {
	paddedKey := padRight(key, 18)
	fmt.Fprintf(w, "    %s%s%s  %s%s%s%s\n", dim, paddedKey, reset, bold, valueColor, value, reset)
}

func printEndpoint(w *os.File, label, method, url, color string) {
	paddedLabel := padRight(label, 8)
	fmt.Fprintf(w, "    %s%s%s %s%s%-5s%s %s%s%s\n",
		dim, paddedLabel, reset,
		bold, brightWhite, method, reset,
		color, url, reset,
	)
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func formatCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%d (%0.1fM)", n, float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%d (%0.1fK)", n, float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func formatCount64(n int64) string {
	return formatCount(int(n))
}

// maskURL strips the path and shows just the scheme+host for compact display.
func maskURL(rawURL string) string {
	if rawURL == "" {
		return "(not set)"
	}
	// Keep URL as is but trim trailing slash
	return strings.TrimRight(rawURL, "/")
}
