package llm

import (
	"context"
	"errors"
	"fmt"

	"github.com/sashabaranov/go-openai"

	"github.com/agent-forge/agent-forge/internal/config"
)

// ErrNilConfig is returned when a nil config is provided.
var ErrNilConfig = errors.New("llm config is nil")

// ErrEmptyResponse is returned when the LLM returns an empty response.
var ErrEmptyResponse = errors.New("llm returned empty response")

// Triple represents a Subject-Predicate-Object knowledge graph triple.
type Triple struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
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

// ExtractTriples uses the LLM to extract knowledge graph triples from text.
func (c *Client) ExtractTriples(ctx context.Context, text string) ([]Triple, error) {
	system := `You are a knowledge extraction expert. Extract factual relationships from the provided text as Subject-Predicate-Object triples.

Rules:
- Extract only factual, verifiable relationships
- Subjects and Objects should be named entities (people, places, organizations, concepts)
- Predicates should be concise verb phrases
- Return ONLY valid JSON array, no explanation
- Format: [{"subject": "X", "predicate": "Y", "object": "Z"}]
- Extract 5-20 triples per chunk
- If no clear triples exist, return []`

	prompt := fmt.Sprintf("Extract knowledge graph triples from this text:\n\n%s", text)

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
