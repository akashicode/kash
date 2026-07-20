package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
)

//go:embed ui.html
var uiHTML []byte

// handleUI serves the embedded dashboard page at the root path.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	// "/" is a catch-all on ServeMux — anything else under it is a 404
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(uiHTML)
}

// docInfo is a per-document summary for the dashboard.
type docInfo struct {
	Name        string    `json:"name"`
	Chunks      int       `json:"chunks"`
	Triples     int       `json:"triples"`
	Done        bool      `json:"done"`
	CompletedAt time.Time `json:"completed_at,omitzero"`
}

// handleAPIInfo returns agent metadata, corpus stats, and the document list.
func (s *Server) handleAPIInfo(w http.ResponseWriter, r *http.Request) {
	docs := []docInfo{}
	if s.buildManifest != nil {
		for name, st := range s.buildManifest.Documents {
			docs = append(docs, docInfo{
				Name:        name,
				Chunks:      st.Chunks,
				Triples:     st.Triples,
				Done:        st.Done(),
				CompletedAt: st.CompletedAt,
			})
		}
		sort.Slice(docs, func(i, j int) bool { return docs[i].Name < docs[j].Name })
	}

	resp := map[string]interface{}{
		"agent":            s.agentCfg.Agent.Name,
		"description":      s.agentCfg.Agent.Description,
		"version":          s.agentCfg.Agent.Version,
		"corpus_version":   s.corpusVersion,
		"vectors":          s.vectorStore.Count(),
		"triples":          s.graphDB.Count(),
		"llm_model":        s.appCfg.LLM.Model,
		"embed_model":      s.appCfg.Embedder.Model,
		"embed_dimensions": s.appCfg.Embedder.Dimensions,
		"reranker_enabled": s.appCfg.Reranker.BaseURL != "",
		"documents":        docs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAPIGraph returns triples for the graph explorer. With ?q= it runs a
// graph search; without it, it returns a uniform sample of the whole graph.
func (s *Server) handleAPIGraph(w http.ResponseWriter, r *http.Request) {
	limit := clampQueryInt(r, "limit", 150, 1, 500)
	query := r.URL.Query().Get("q")

	ctx := r.Context()
	var (
		results interface{}
		err     error
	)
	if query != "" {
		results, err = s.graphDB.Search(ctx, query, limit)
	} else {
		results, err = s.graphDB.Sample(ctx, limit)
	}
	if err != nil {
		http.Error(w, "graph query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"triples": results})
}

// handleAPISearch runs hybrid retrieval and returns structured results —
// the same pipeline the chat endpoint uses, exposed for inspection.
func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "q parameter is required", http.StatusBadRequest)
		return
	}
	topK := clampQueryInt(r, "top_k", defaultTopK, 1, 20)

	// Bound retrieval time — reranker and embedder are remote calls
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := s.retrieve(ctx, query, topK)
	if err != nil {
		http.Error(w, "search failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// clampQueryInt reads an integer query parameter with a default and bounds.
func clampQueryInt(r *http.Request, name string, def, min, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
