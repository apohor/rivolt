// Google Gemini generateContent API provider.
//
// Docs: https://ai.google.dev/api/generate-content
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const defaultGeminiBase = "https://generativelanguage.googleapis.com/v1beta"

// GeminiProvider calls the Google Generative Language API.
type GeminiProvider struct {
	apiKey string
	model  string
	base   string
	client *http.Client
}

// GeminiConfig configures the provider.
type GeminiConfig struct {
	APIKey   string // required
	Model    string // default "gemini-1.5-flash"
	Endpoint string // v1beta base URL; default official endpoint
	Timeout  time.Duration
}

// NewGemini constructs a provider. Returns an error if no API key is set.
func NewGemini(cfg GeminiConfig) (*GeminiProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("gemini: api key is required")
	}
	if cfg.Model == "" {
		cfg.Model = "gemini-2.5-flash"
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultGeminiBase
	}
	if cfg.Timeout <= 0 {
		// Reasoning models (gemini-3-pro-preview, etc.) routinely take
		// 60-120s on first-token. 3 minutes covers them without being silly.
		cfg.Timeout = 3 * time.Minute
	}
	return &GeminiProvider{
		apiKey: cfg.APIKey,
		model:  cfg.Model,
		base:   cfg.Endpoint,
		client: &http.Client{Timeout: cfg.Timeout},
	}, nil
}

// Name reports a stable identifier for cache keying.
func (p *GeminiProvider) Name() string { return "gemini:" + p.model }

// Complete sends the prompt pair and returns the assistant text along with
// real token usage from the response.
//
// Gemini has no separate "system" role; we fold it into the request as
// system_instruction which is the documented equivalent.
func (p *GeminiProvider) Complete(ctx context.Context, system, user string) (string, TokenUsage, error) {
	reqBody := map[string]any{
		"system_instruction": map[string]any{
			"parts": []map[string]string{{"text": system}},
		},
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": []map[string]string{{"text": user}},
			},
		},
		"generationConfig": map[string]any{
			"temperature": 0.4,
			// Gemini 2.5 models consume output budget for internal reasoning
			// before emitting the visible answer, so we need a generous cap.
			"maxOutputTokens": 4096,
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("encode request: %w", err)
	}

	// Pass the API key via header, NOT the URL query string. Go's net/http
	// includes the full URL in error messages (including ?key=...), which
	// leaks the key into slog output if the request errors out. Gemini
	// accepts both, so always prefer the header.
	endpoint := fmt.Sprintf("%s/models/%s:generateContent",
		p.base, url.PathEscape(p.model))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", TokenUsage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", TokenUsage{}, fmt.Errorf("gemini: %w", err)
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
		return "", TokenUsage{}, fmt.Errorf("gemini: http %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", TokenUsage{}, fmt.Errorf("decode response: %w", err)
	}
	var out bytes.Buffer
	for _, c := range parsed.Candidates {
		for _, part := range c.Content.Parts {
			out.WriteString(part.Text)
		}
	}
	if out.Len() == 0 {
		return "", TokenUsage{}, fmt.Errorf("gemini: empty response")
	}
	return out.String(), TokenUsage{
		InputTokens:  parsed.UsageMetadata.PromptTokenCount,
		OutputTokens: parsed.UsageMetadata.CandidatesTokenCount,
	}, nil
}
