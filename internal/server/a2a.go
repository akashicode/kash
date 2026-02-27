package server

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// A2ARequest is an Agent-to-Agent JSON-RPC request.
type A2ARequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// A2AResponse is an Agent-to-Agent JSON-RPC response.
type A2AResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *A2AError   `json:"error,omitempty"`
}

// A2AError represents a JSON-RPC error.
type A2AError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleA2A handles POST /rpc/agent — the Agent-to-Agent JSON-RPC endpoint.
func (s *Server) handleA2A(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req A2ARequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeA2AError(w, nil, -32700, "parse error: "+err.Error())
		return
	}

	var result interface{}
	var rpcErr *A2AError

	switch req.Method {
	case "agent.info":
		result = s.a2aAgentInfo()
	case "agent.query":
		result, rpcErr = s.a2aQuery(r, req.Params)
	case "agent.search":
		result, rpcErr = s.a2aSearch(r, req.Params)
	default:
		rpcErr = &A2AError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}
	}

	resp := A2AResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
		Error:   rpcErr,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// a2aAgentInfo returns metadata about this agent.
func (s *Server) a2aAgentInfo() map[string]interface{} {
	tools := s.buildMCPTools()
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Name
	}

	return map[string]interface{}{
		"name":        s.agentCfg.Agent.Name,
		"description": s.agentCfg.Agent.Description,
		"version":     "1.0.0",
		"capabilities": map[string]interface{}{
			"query":  true,
			"search": true,
			"stream": false,
		},
		"tools":   toolNames,
		"vectors": s.vectorStore.Count(),
		"triples": s.graphDB.Count(),
		"endpoints": map[string]string{
			"rest": "/v1/chat/completions",
			"mcp":  "/mcp",
			"a2a":  "/rpc/agent",
		},
	}
}

// a2aQuery handles agent.query — a full chat-style query with context injection.
func (s *Server) a2aQuery(r *http.Request, params json.RawMessage) (interface{}, *A2AError) {
	var p struct {
		Query        string                   `json:"query"`
		SystemPrompt string                   `json:"system_prompt,omitempty"`
		History      []map[string]interface{} `json:"history,omitempty"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &A2AError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	if p.Query == "" {
		return nil, &A2AError{Code: -32602, Message: "query is required"}
	}

	ctx := r.Context()

	// Run hybrid search
	retrievedCtx, err := s.hybridSearch(ctx, p.Query)
	if err != nil {
		retrievedCtx = ""
	}

	// Build messages
	systemPrompt := s.agentCfg.Agent.SystemPrompt
	if p.SystemPrompt != "" {
		systemPrompt = p.SystemPrompt
	}

	var messages []map[string]string
	if systemPrompt != "" {
		messages = append(messages, map[string]string{"role": "system", "content": systemPrompt})
	}
	if retrievedCtx != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": "Retrieved context:\n\n" + retrievedCtx,
		})
	}
	messages = append(messages, map[string]string{"role": "user", "content": p.Query})

	// Call LLM (simplified via Complete)
	answer, err := s.llmClient.Complete(ctx, systemPrompt+"\n\n"+retrievedCtx, p.Query)
	if err != nil {
		s.log.Error("A2A LLM call failed", "error", err)
		return nil, &A2AError{Code: -32603, Message: "upstream LLM request failed"}
	}

	return map[string]interface{}{
		"answer":  answer,
		"context": retrievedCtx,
		"agent":   s.agentCfg.Agent.Name,
	}, nil
}

// a2aSearch handles agent.search — raw knowledge retrieval without LLM.
func (s *Server) a2aSearch(r *http.Request, params json.RawMessage) (interface{}, *A2AError) {
	var p struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k,omitempty"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &A2AError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	if p.Query == "" {
		return nil, &A2AError{Code: -32602, Message: "query is required"}
	}
	if p.TopK <= 0 {
		p.TopK = 5
	}

	ctx := r.Context()

	vectorResults, err := s.vectorStore.Query(ctx, p.Query, p.TopK)
	if err != nil {
		return nil, &A2AError{Code: -32603, Message: "vector search error: " + err.Error()}
	}

	graphResults, _ := s.graphDB.Search(ctx, p.Query, p.TopK*2)

	results := make([]map[string]interface{}, len(vectorResults))
	for i, r := range vectorResults {
		results[i] = map[string]interface{}{
			"content":    r.Content,
			"source":     r.Source,
			"similarity": r.Similarity,
		}
	}

	return map[string]interface{}{
		"vector_results": results,
		"graph_results":  graphResults,
		"query":          p.Query,
	}, nil
}

func writeA2AError(w http.ResponseWriter, id interface{}, code int, msg string) {
	resp := A2AResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &A2AError{Code: code, Message: msg},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
