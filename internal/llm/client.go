package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sashabaranov/go-openai"

	"github.com/akashicode/kash/internal/config"
)

// ErrNilConfig is returned when a nil config is provided.
var ErrNilConfig = errors.New("llm config is nil")

// ErrEmptyResponse is returned when the LLM returns an empty response.
var ErrEmptyResponse = errors.New("llm returned empty response")

// Triple represents a Subject-Predicate-Object knowledge graph triple.
type Triple struct {
	Subject     string `json:"subject"`
	Predicate   string `json:"predicate"`
	Object      string `json:"object"`
	Description string `json:"description,omitempty"`
	Passage     int    `json:"passage,omitempty"`
	ChunkID     string `json:"chunk_id,omitempty"`
}

// DecomposedQuery contains low-level and high-level query keywords
// extracted by the LLM for dual-channel retrieval.
type DecomposedQuery struct {
	SpecificEntities []string `json:"specific_entities"`
	BroadConcepts    []string `json:"broad_concepts"`
}

// Client wraps the OpenAI client for LLM interactions.
type Client struct {
	client *openai.Client
	model  string
}

// NewClient creates a new LLM client from a ProviderConfig.
func NewClient(cfg *config.ProviderConfig) (*Client, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("llm base_url is required")
	}
	if cfg.APIKey == "" {
		return nil, errors.New("llm api_key is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("llm model is required")
	}

	clientCfg := openai.DefaultConfig(cfg.APIKey)
	clientCfg.BaseURL = cfg.BaseURL

	return &Client{
		client: openai.NewClientWithConfig(clientCfg),
		model:  cfg.Model,
	}, nil
}

// Complete sends a single user message and returns the assistant response text.
func (c *Client) Complete(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	messages := []openai.ChatCompletionMessage{}
	if systemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		})
	}
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userMessage,
	})

	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: messages,
	})
	if err != nil {
		return "", fmt.Errorf("chat completion: %w", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", ErrEmptyResponse
	}
	return resp.Choices[0].Message.Content, nil
}

// ExtractionSpec describes the corpus-specific vocabulary the extractor must
// use. Supplied from agent.yaml so the same code serves any subject matter.
type ExtractionSpec struct {
	// Predicates is the closed vocabulary of allowed relations.
	Predicates []string
	// Priorities lists the relation types to favour, most important first.
	Priorities []string
}

// ExtractTriples uses the LLM to extract knowledge graph triples from text.
// The text may contain several delimited passages; relationships must not be
// inferred across passage boundaries (see the provenance rule below).
func (c *Client) ExtractTriples(ctx context.Context, text string, spec ExtractionSpec) ([]Triple, error) {
	if len(spec.Predicates) == 0 {
		return nil, errors.New("extraction predicate vocabulary is empty")
	}

	var priorities strings.Builder
	for i, p := range spec.Priorities {
		fmt.Fprintf(&priorities, "%d. %s\n", i+1, p)
	}
	if priorities.Len() == 0 {
		priorities.WriteString("1. Relations that connect two named entities.\n")
	}

	system := `You are a knowledge graph extraction expert.
Extract factual relationships that are EXPLICITLY STATED in the passages as Subject-Predicate-Object triples.

PRIORITIZE, in this order:
` + priorities.String() + `
Never skip a stated relation between two named entities in favour of trivia.

CRITICAL RULES:
- Extract ONLY what a passage explicitly states. Never infer, never guess.
- DO NOT CONFLATE PROVENANCE WITH CONTENT. Title pages, colophons, publisher blurbs
  and translator credits describe the document you are reading — not other works that
  happen to be mentioned inside it. If a passage credits a translator or editor, that
  credit belongs ONLY to the work that passage is about. Never bind a translator,
  editor, commentator or publisher to a different text merely because both appear
  nearby. If the passage does not literally assert "A translated B", do not emit it.
- Each passage below is a SEPARATE excerpt. Do not combine facts across passages.
- Skip commercial metadata entirely: distributors, booksellers, prices, ISBNs, print runs.
- Use the shortest unambiguous name for an entity, and use it consistently
  (e.g. "Abhinavagupta", not "the great master Abhinavagupta").
- Never emit the same fact twice with different wording.

PREDICATE VOCABULARY — this list is CLOSED. Every triple MUST use one of these
exact predicate strings, in English, whatever language the source text is in.
Choose the closest fit. If no predicate fits, DROP the fact rather than
inventing a new predicate:
  ` + strings.Join(spec.Predicates, ", ") + `
Do not use vague predicates like "is" or "is a" — use the closest specific one
from the list above. Never emit a non-English predicate.

OUTPUT:
- Return ONLY a valid JSON array, no explanation, no markdown fences.
- Format: [{"subject": "X", "predicate": "Y", "object": "Z", "passage": 1}]
  where "passage" is the 1-based index of the passage from which the fact was extracted (e.g. 1 for PASSAGE 1).
- Extract 5-20 triples. If nothing is explicitly stated, return [].`

	prompt := fmt.Sprintf("Extract knowledge graph triples from these passages:\n\n%s", text)

	raw, err := c.Complete(ctx, system, prompt)
	if err != nil {
		return nil, fmt.Errorf("extract triples: %w", err)
	}

	triples, err := parseTriples(raw)
	if err != nil {
		return nil, fmt.Errorf("parse triples response: %w", err)
	}
	return triples, nil
}

// GenerateMCPDescription generates an optimized MCP tool description for a knowledge base.
func (c *Client) GenerateMCPDescription(ctx context.Context, agentName, sampleContent string) (string, error) {
	system := `You are an expert at writing Model Context Protocol (MCP) tool descriptions.
Write a concise, highly effective tool description that:
1. Clearly explains what domain knowledge the tool provides
2. Lists 3-5 specific topic areas covered
3. Guides the AI on when to call this tool
4. Is 2-4 sentences maximum
Return ONLY the description text, nothing else.`

	prompt := fmt.Sprintf(`Write an MCP tool description for an AI agent named "%s" 
that has been trained on the following knowledge (sample):

%s

The tool name will be: search_%s_knowledge`, agentName, sampleContent, agentName)

	desc, err := c.Complete(ctx, system, prompt)
	if err != nil {
		return "", fmt.Errorf("generate MCP description: %w", err)
	}
	return desc, nil
}

// RewriteQuery rewrites a conversational follow-up ("tell me more about
// that") into a standalone search query by resolving references against the
// recent conversation history. Returns the rewritten query, or an error the
// caller should treat as non-fatal (fall back to the original message).
func (c *Client) RewriteQuery(ctx context.Context, history []openai.ChatCompletionMessage, lastMessage string) (string, error) {
	system := `You rewrite conversational follow-up messages into standalone search queries.
Given a conversation and the user's latest message, produce ONE self-contained search query that captures what the user is asking about, resolving pronouns and references like "that", "it", or "tell me more".
Rules:
- Return ONLY the query text — no quotes, no explanation, no punctuation-only output
- Keep it short and keyword-rich (it feeds a search engine, not a chat)
- If the latest message is already self-contained, return it unchanged`

	var sb strings.Builder
	for _, m := range history {
		content := m.Content
		// Truncate long turns — only the topic matters for rewriting
		if r := []rune(content); len(r) > 500 {
			content = string(r[:500]) + "…"
		}
		fmt.Fprintf(&sb, "%s: %s\n", m.Role, content)
	}

	prompt := fmt.Sprintf("Conversation so far:\n%s\nLatest user message: %s\n\nStandalone search query:", sb.String(), lastMessage)

	out, err := c.Complete(ctx, system, prompt)
	if err != nil {
		return "", fmt.Errorf("rewrite query: %w", err)
	}
	out = strings.TrimSpace(strings.Trim(strings.TrimSpace(out), `"`))
	if out == "" {
		return "", ErrEmptyResponse
	}
	return out, nil
}

// ChatWithContext proxies a chat completion request, injecting context into the system message.
func (c *Client) ChatWithContext(ctx context.Context, messages []openai.ChatCompletionMessage, retrievedContext string) (string, error) {
	augmented := make([]openai.ChatCompletionMessage, 0, len(messages)+1)

	// Inject retrieved context as first system message
	if retrievedContext != "" {
		augmented = append(augmented, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleSystem,
			Content: fmt.Sprintf(`You have access to the following relevant knowledge retrieved from the expert knowledge base.
Use this information to provide accurate, grounded responses.

--- RETRIEVED CONTEXT ---
%s
--- END CONTEXT ---`, retrievedContext),
		})
	}
	augmented = append(augmented, messages...)

	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:    c.model,
		Messages: augmented,
	})
	if err != nil {
		return "", fmt.Errorf("chat with context: %w", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", ErrEmptyResponse
	}
	return resp.Choices[0].Message.Content, nil
}

// ChatCompletionStream handles streaming chat completions.
func (c *Client) ChatCompletionStream(ctx context.Context, req openai.ChatCompletionRequest, handler func(delta string) error) error {
	req.Model = c.model
	req.Stream = true

	stream, err := c.client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return fmt.Errorf("create stream: %w", err)
	}
	defer stream.Close()

	for {
		response, err := stream.Recv()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// io.EOF signals end of stream
			if err.Error() == "EOF" {
				return nil
			}
			return fmt.Errorf("stream recv: %w", err)
		}
		if len(response.Choices) > 0 {
			delta := response.Choices[0].Delta.Content
			if delta != "" {
				if err := handler(delta); err != nil {
					return err
				}
			}
		}
	}
}

// Model returns the configured model name.
func (c *Client) Model() string {
	return c.model
}

// DecomposeQuery extracts specific entities (low-level keywords) and broad
// conceptual themes (high-level keywords) from a query for dual-channel retrieval.
func (c *Client) DecomposeQuery(ctx context.Context, query string) (DecomposedQuery, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return DecomposedQuery{}, nil
	}

	system := `You are a query analysis assistant for a knowledge retrieval system.
Your task is to decompose a user query into two lists of keywords:
1. specific_entities: named entities, people, texts, places, technical identifiers, or specific components directly mentioned.
2. broad_concepts: general themes, domains, high-level topics, categories, or conceptual domains.

Respond ONLY with a JSON object in this exact format:
{
  "specific_entities": ["..."],
  "broad_concepts": ["..."]
}
If there are no specific entities or broad concepts, use an empty array [].
Do not include conversational filler ("what", "who", "tell me", "difference between").`

	prompt := fmt.Sprintf("Query: %s", trimmed)
	raw, err := c.Complete(ctx, system, prompt)
	if err != nil {
		return DecomposedQuery{}, fmt.Errorf("decompose query: %w", err)
	}

	return parseDecomposedQuery(raw)
}
