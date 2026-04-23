// Package ai hosts the hot-swappable LLM provider adapters (OpenAI,
// Anthropic, Google Gemini) used across Rivolt: weekly digest, trip
// planning, charging strategy, and anomaly explanations.
//
// The code in this file is provider-agnostic. Domain features (digest,
// planner, coach) live in their own files and build on top of Provider.
package ai

import (
	"context"
)

// Provider is the minimal contract every LLM backend must implement.
type Provider interface {
	// Complete sends a system+user prompt pair and returns the assistant
	// text along with real token usage parsed from the provider response.
	// Implementations MUST return usage whenever the API gives it to them;
	// a zero-valued TokenUsage is only acceptable when the upstream
	// response omitted the counts (fall back to zeros, never estimate).
	Complete(ctx context.Context, system, user string) (string, TokenUsage, error)
	// Name returns a short identifier (e.g. "openai:gpt-4o-mini") used
	// for cache keys and usage accounting. Changing model invalidates
	// downstream caches automatically.
	Name() string
}

// TokenUsage is the real input/output token count reported by a provider
// for a single call. Zero values mean the provider didn't report counts.
type TokenUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Analyzer wraps a Provider. It exists so call sites can hot-swap the
// backing provider without plumbing the raw Provider through every
// handler. Settings manager owns the single *Analyzer instance and
// rebuilds it when the operator changes API key / provider / model.
type Analyzer struct {
	provider Provider
}

// NewAnalyzer returns an analyzer backed by p. p must be non-nil; the
// settings manager guards against that before calling.
func NewAnalyzer(p Provider) *Analyzer {
	return &Analyzer{provider: p}
}

// Provider returns the underlying Provider so callers that need full
// control (custom prompts, streaming) can reach through.
func (a *Analyzer) Provider() Provider { return a.provider }

// ModelName is the "provider:model" short identifier used in logs.
func (a *Analyzer) ModelName() string {
	if a == nil || a.provider == nil {
		return ""
	}
	return a.provider.Name()
}

// Complete is a convenience passthrough so domain code doesn't have to
// reach into Provider directly.
func (a *Analyzer) Complete(ctx context.Context, system, user string) (string, TokenUsage, error) {
	return a.provider.Complete(ctx, system, user)
}
