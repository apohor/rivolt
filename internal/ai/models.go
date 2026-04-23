// Model listing helpers — one per provider. Each returns the set of model
// IDs that can serve generateContent / chat completions, so the UI can offer
// a dropdown instead of asking the operator to type a model name.
//
// These are plain functions (not methods on *Provider) because the settings
// UI needs to list models even when the selected model would otherwise be
// invalid — we don't want to construct a full provider just to list.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ListOpenAIModels returns the subset of OpenAI models usable with chat
// completions. We filter client-side since /v1/models returns every model
// including embeddings, moderation, TTS, etc.
func ListOpenAIModels(ctx context.Context, apiKey string) ([]string, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("openai: api key required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	raw, err := doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("openai: decode: %w", err)
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if isOpenAIChatModel(m.ID) {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// isOpenAIChatModel keeps the list reasonable: gpt-* and o-series chat
// models. Everything else (embeddings, dall-e, whisper, tts, moderation,
// fine-tuning checkpoints) is filtered out.
func isOpenAIChatModel(id string) bool {
	if strings.Contains(id, "embedding") || strings.Contains(id, "whisper") ||
		strings.Contains(id, "tts") || strings.Contains(id, "dall-e") ||
		strings.Contains(id, "moderation") || strings.Contains(id, "image") ||
		strings.Contains(id, "audio") || strings.Contains(id, "realtime") ||
		strings.Contains(id, "transcribe") || strings.HasPrefix(id, "ft:") {
		return false
	}
	return strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "o1") ||
		strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4") ||
		strings.HasPrefix(id, "chatgpt-")
}

// ListAnthropicModels returns every model the Anthropic Messages API
// advertises. All of them support messages, so no client-side filtering.
func ListAnthropicModels(ctx context.Context, apiKey string) ([]string, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("anthropic: api key required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", defaultAnthropicVersion)
	raw, err := doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("anthropic: decode: %w", err)
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		out = append(out, m.ID)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out))) // newest-first-ish
	return out, nil
}

// ListGeminiModels returns the Gemini models that support generateContent,
// with the "models/" prefix stripped. Gemma and preview-only variants are
// included too — the user can pick, we don't gatekeep.
func ListGeminiModels(ctx context.Context, apiKey string) ([]string, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("gemini: api key required")
	}
	req, err := http.NewRequestWithContext(ctx,
		http.MethodGet,
		"https://generativelanguage.googleapis.com/v1beta/models",
		nil)
	if err != nil {
		return nil, err
	}
	// Header auth keeps the key out of URL-based error messages / logs.
	req.Header.Set("x-goog-api-key", apiKey)
	raw, err := doJSON(req)
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	var parsed struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("gemini: decode: %w", err)
	}
	out := make([]string, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if !contains(m.SupportedGenerationMethods, "generateContent") {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
		// Skip image / tts / embedding variants even if they advertise
		// generateContent — they won't work for a text critique.
		if strings.Contains(id, "image") || strings.Contains(id, "tts") ||
			strings.Contains(id, "embedding") || strings.Contains(id, "lyria") ||
			strings.Contains(id, "banana") {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// doJSON is a small shared helper so the three list functions don't have to
// duplicate client/timeout/error-body-truncation logic.
func doJSON(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		if len(raw) > 400 {
			raw = raw[:400]
		}
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
}
