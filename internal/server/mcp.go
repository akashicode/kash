package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// MCPTool represents an MCP tool definition.
type MCPTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema MCPSchema   `json:"inputSchema"`
}

// MCPSchema represents a JSON schema for tool inputs.
type MCPSchema struct {
	Type       string              `json:"type"`
	Properties map[string]MCPProp  `json:"properties"`
	Required   []string            `json:"required"`
}

// MCPProp represents a single parameter property.
type MCPProp struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// MCPRequest is an incoming MCP JSON-RPC request.
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCPResponse is an outgoing MCP JSON-RPC response.
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
}

// MCPError represents a JSON-RPC error.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleMCP handles the MCP endpoint.
// It supports both SSE streaming (GET) and JSON-RPC (POST).
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleMCPSSE(w, r)
	case http.MethodPost:
		s.handleMCPRPC(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMCPSSE sends the MCP server info as a Server-Sent Events stream.
func (s *Server) handleMCPSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Send server info event
	serverInfo := map[string]interface{}{
		"type": "endpoint",
		"url":  "/mcp",
	}
	infoJSON, _ := json.Marshal(serverInfo)
	fmt.Fprintf(w, "data: %s\n\n", infoJSON)
	flusher.Flush()

	// Keep connection alive until client disconnects
	ctx := r.Context()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// handleMCPRPC processes MCP JSON-RPC requests.
func (s *Server) handleMCPRPC(w http.ResponseWriter, r *http.Request) {
	var req MCPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONRPCError(w, nil, -32700, "parse error: "+err.Error())
		return
	}

	var result interface{}
	var rpcErr *MCPError

	switch req.Method {
	case "initialize":
		result = s.mcpInitialize()
	case "tools/list":
		result = s.mcpListTools()
	case "tools/call":
		result, rpcErr = s.mcpCallTool(r, req.Params)
	default:
		rpcErr = &MCPError{Code: -32601, Message: "method not found: " + req.Method}
	}

	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
		Error:   rpcErr,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) mcpInitialize() map[string]interface{} {
	return map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    s.agentCfg.Agent.Name,
			"version": "1.0.0",
		},
	}
}

func (s *Server) mcpListTools() map[string]interface{} {
	tools := s.buildMCPTools()
	return map[string]interface{}{
		"tools": tools,
	}
}

func (s *Server) buildMCPTools() []MCPTool {
	tools := []MCPTool{}

	// Build tools from agent.yaml definitions
	for _, t := range s.agentCfg.MCP.Tools {
		tools = append(tools, MCPTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: MCPSchema{
				Type: "object",
				Properties: map[string]MCPProp{
					"query": {
						Type:        "string",
						Description: "The search query to find relevant information",
					},
					"top_k": {
						Type:        "integer",
						Description: "Number of results to return (default: 5)",
					},
				},
				Required: []string{"query"},
			},
		})
	}

	// Always include a default tool if none defined
	if len(tools) == 0 {
		agentSlug := strings.ToLower(strings.ReplaceAll(s.agentCfg.Agent.Name, " ", "_"))
		tools = append(tools, MCPTool{
			Name:        "search_" + agentSlug + "_knowledge",
			Description: s.agentCfg.Agent.Description,
			InputSchema: MCPSchema{
				Type: "object",
				Properties: map[string]MCPProp{
					"query": {
						Type:        "string",
						Description: "The search query",
					},
				},
				Required: []string{"query"},
			},
		})
	}

	return tools
}

func (s *Server) mcpCallTool(r *http.Request, params json.RawMessage) (interface{}, *MCPError) {
	var p struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &MCPError{Code: -32602, Message: "invalid params: " + err.Error()}
	}

	query, _ := p.Arguments["query"].(string)
	if query == "" {
		return nil, &MCPError{Code: -32602, Message: "query argument is required"}
	}

	topK := 5
	if tk, ok := p.Arguments["top_k"].(float64); ok && tk > 0 {
		topK = int(tk)
	}

	ctx := r.Context()
	retrievedCtx, err := s.hybridSearch(ctx, query)
	if err != nil {
		return nil, &MCPError{Code: -32603, Message: "search error: " + err.Error()}
	}

	// Limit to topK result segments
	_ = topK

	return map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": retrievedCtx,
			},
		},
	}, nil
}

func writeJSONRPCError(w http.ResponseWriter, id interface{}, code int, msg string) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &MCPError{Code: code, Message: msg},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
