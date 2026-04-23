// Anthropic Claude Messages API provider.
//
// Docs: https://docs.anthropic.com/en/api/messages
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

const defaultAnthropicEndpoint = "https://api.anthropic.com/v1/messages"
const defaultAnthropicVersion = "2023-06-01"

// AnthropicProvider calls the Anthropic Messages API.
type AnthropicProvider struct {
	apiKey   string
	model    string
	endpoint string
	version  string
	client   *http.Client
}

// AnthropicConfig configures the provider. Zero values pick sensible defaults.
type AnthropicConfig struct {
	APIKey   string // required
	Model    string // default "claude-3-5-haiku-latest"
	Endpoint string
	Version  string // x-api-key header version, default "2023-06-01"
	Timeout  time.Duration
}

// NewAnthropic constructs a provider. Returns an error if no API key is set.
func NewAnthropic(cfg AnthropicConfig) (*AnthropicProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("anthropic: api key is required")
	}
	if cfg.Model == "" {
		cfg.Model = "claude-haiku-4-5-20251001"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultAnthropicEndpoint
	}
	if cfg.Version == "" {
		cfg.Version = defaultAnthropicVersion
	}
	if cfg.Timeout <= 0 {
		// Claude with extended thinking routinely exceeds 60s on first-token.
		// 3 minutes covers it without being silly.
		cfg.Timeout = 3 * time.Minute
	}
	return &AnthropicProvider{
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		endpoint: cfg.Endpoint,
		version:  cfg.Version,
		client:   &http.Client{Timeout: cfg.Timeout},
	}, nil
}

// Name reports a stable identifier for cache keying.
func (p *AnthropicProvider) Name() string { return "anthropic:" + p.model }

// Complete sends a system+user prompt pair and returns the assistant text
// along with real token usage parsed from the Anthropic response.
func (p *AnthropicProvider) Complete(ctx context.Context, system, user string) (string, TokenUsage, error) {
	reqBody := map[string]any{
		"model":      p.model,
		"max_tokens": 1024,
		"system":     system,
		"messages": []map[string]any{
			{"role": "user", "content": user},
		},
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
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", p.version)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("anthropic: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", TokenUsage{}, err
	}
	if resp.StatusCode/100 != 2 {
		if len(raw) > 500 {
			raw = raw[:500]
		}
		return "", TokenUsage{}, fmt.Errorf("anthropic: http %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", TokenUsage{}, fmt.Errorf("decode response: %w", err)
	}
	// Concatenate all text blocks (messages API can return several).
	var out bytes.Buffer
	for _, c := range parsed.Content {
		if c.Type == "text" {
			out.WriteString(c.Text)
		}
	}
	if out.Len() == 0 {
		return "", TokenUsage{}, fmt.Errorf("anthropic: empty response")
	}
	return out.String(), TokenUsage{
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
	}, nil
}
