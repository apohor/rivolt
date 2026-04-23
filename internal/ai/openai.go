// OpenAI Chat Completions provider. No SDK — one tiny HTTP call keeps the
// dependency surface small and makes auditing trivial.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultOpenAIEndpoint = "https://api.openai.com/v1/chat/completions"

// OpenAIProvider calls the OpenAI Chat Completions API.
type OpenAIProvider struct {
	apiKey   string
	model    string
	endpoint string
	client   *http.Client
}

// OpenAIConfig configures the provider. Zero values pick sensible defaults.
type OpenAIConfig struct {
	APIKey   string // required
	Model    string // default "gpt-4o-mini"
	Endpoint string // default official OpenAI chat endpoint
	Timeout  time.Duration
}

// NewOpenAI constructs a provider. Returns an error if no API key is set.
func NewOpenAI(cfg OpenAIConfig) (*OpenAIProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("openai: api key is required")
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultOpenAIEndpoint
	}
	if cfg.Timeout <= 0 {
		// Reasoning models (o1, o3-mini, gpt-5-thinking) routinely exceed
		// 60s on first-token. 3 minutes covers them without being silly.
		cfg.Timeout = 3 * time.Minute
	}
	return &OpenAIProvider{
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		endpoint: cfg.Endpoint,
		client:   &http.Client{Timeout: cfg.Timeout},
	}, nil
}

// Name reports a stable identifier for cache keying.
func (p *OpenAIProvider) Name() string { return "openai:" + p.model }

// Complete sends a system+user prompt pair and returns the assistant text
// along with real token usage from the response.
func (p *OpenAIProvider) Complete(ctx context.Context, system, user string) (string, TokenUsage, error) {
	reqBody := map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.4,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", TokenUsage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", TokenUsage{}, err
	}
	if resp.StatusCode/100 != 2 {
		// Trim the body so we don't leak huge error payloads into logs.
		if len(raw) > 500 {
			raw = raw[:500]
		}
		return "", TokenUsage{}, fmt.Errorf("openai: http %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", TokenUsage{}, fmt.Errorf("decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", TokenUsage{}, fmt.Errorf("openai: empty response")
	}
	return parsed.Choices[0].Message.Content, TokenUsage{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}, nil
}
