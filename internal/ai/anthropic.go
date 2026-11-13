// Anthropic Claude provider on the official Go SDK.
//
// Docs: https://docs.anthropic.com/en/api/messages
package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// defaultAnthropicVersion is the anthropic-version header the model listing
// (models.go) still sends by hand; the SDK sets its own for Complete.
const defaultAnthropicVersion = "2023-06-01"

// OperatorModel is the model the operator key runs on. Users' own keys keep
// whatever model they have configured.
const OperatorModel = "claude-opus-5-5"

// operatorRouteModels is the model each route runs on under the operator key, from the
// model evaluation (_shared/ai-model-eval.md): Haiku is enough for the short recap, the
// drive-efficiency attribution and the trip advice need Sonnet's reasoning, and Opus
// cost 2 to 4 times more for no consistent gain. A route not listed runs on OperatorModel.
var operatorRouteModels = map[Route]string{
	RouteRecap:      "claude-haiku-5-5",
	RouteEfficiency: "claude-sonnet-5-5",
	RouteAdvice:     "claude-sonnet-5-5",
}

// OperatorModelFor is the model the operator key uses for a route.
func OperatorModelFor(r Route) string {
	if m, ok := operatorRouteModels[r]; ok {
		return m
	}
	return OperatorModel
}

// Route names the feature a Complete call serves. It picks the effort and the
// max_tokens, and labels the operator token counter. Callers set it with
// WithRoute; an unset route is RouteOther.
type Route string

const (
	RouteRecap      Route = "recap"
	RouteEfficiency Route = "efficiency"
	RouteAdvice     Route = "advice"
	RoutePing       Route = "ping"
	RouteOther      Route = "other"
)

type routeCtxKey struct{}
type reservedCtxKey struct{}

// WithRoute tags ctx with the route of the call about to be made.
func WithRoute(ctx context.Context, r Route) context.Context {
	return context.WithValue(ctx, routeCtxKey{}, r)
}

// RouteFrom returns the route on ctx, or RouteOther.
func RouteFrom(ctx context.Context) Route {
	if r, ok := ctx.Value(routeCtxKey{}).(Route); ok && r != "" {
		return r
	}
	return RouteOther
}

// WithReservation marks ctx as carrying a reserved unit of the per-user daily
// AI budget for a user who has consented to AI. Only the operator key checks
// it: a call on the operator's money without a reservation is refused, so a
// background job or a route that forgot the budget gate never spends it.
func WithReservation(ctx context.Context) context.Context {
	return context.WithValue(ctx, reservedCtxKey{}, true)
}

// Reserved reports whether ctx carries a budget reservation (see WithReservation).
func Reserved(ctx context.Context) bool {
	v, _ := ctx.Value(reservedCtxKey{}).(bool)
	return v
}

var (
	// ErrRefused means the model declined the request (stop_reason refusal).
	// The message is safe to show; callers must not retry.
	ErrRefused = errors.New("AI unavailable for this request")
	// ErrTruncated means the answer hit max_tokens, so any JSON in it is cut.
	ErrTruncated = errors.New("AI answer was cut off before it finished")
	// ErrOperatorDenied means the operator key was asked to spend without a
	// reserved budget unit.
	ErrOperatorDenied = errors.New("AI unavailable: no budget reserved for this request")
)

// OperatorUsage is one model hop of an operator-key call, for metrics. Counts
// only; no prompt or answer text.
type OperatorUsage struct {
	Route      Route
	Model      string // fixed set, see ServedModelLabel
	Input      int64  // uncached input tokens
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// OperatorObserver receives the usage of every operator-key call.
type OperatorObserver func(OperatorUsage)

type routeParams struct {
	effort    anthropic.BetaOutputConfigEffort
	maxTokens int64
	// total bounds the whole call including SDK retries; attempt bounds one try.
	// total stays under Cloudflare's 100 s edge timeout.
	total, attempt time.Duration
}

// Opus 5.5 thinks by default and thinking tokens count toward max_tokens, so
// these leave room for it. Effort is the only depth control.
var routeTable = map[Route]routeParams{
	RouteRecap:      {anthropic.BetaOutputConfigEffortLow, 4096, 90 * time.Second, 45 * time.Second},
	RouteEfficiency: {anthropic.BetaOutputConfigEffortMedium, 6000, 90 * time.Second, 45 * time.Second},
	RouteAdvice:     {anthropic.BetaOutputConfigEffortMedium, 6000, 90 * time.Second, 45 * time.Second},
	RoutePing:       {anthropic.BetaOutputConfigEffortLow, 2048, 20 * time.Second, 10 * time.Second},
	RouteOther:      {anthropic.BetaOutputConfigEffortMedium, 4096, 90 * time.Second, 45 * time.Second},
}

// modelFeatures is the allowlist of models that take output_config.effort and
// the server-side fallback. Anything else gets the plain request, so a user on
// an older stored model never meets a 400 for a parameter it does not know.
func modelFeatures(model string) (effort, fallback bool) {
	for _, p := range []string{"claude-opus-5", "claude-fable-5"} {
		if strings.HasPrefix(model, p) {
			return true, true
		}
	}
	// Sonnet 5.5 and Haiku 5.5 take effort (as run in the model evaluation) but not the fallback beta.
	for _, p := range []string{"claude-sonnet-5-5", "claude-haiku-5-5"} {
		if model == p || strings.HasPrefix(model, p+"-") {
			return true, false
		}
	}
	return false, false
}

// ServedModelLabel maps the model id a response reports to a fixed label set
// so the metric's cardinality stays bounded.
func ServedModelLabel(model string) string {
	for _, p := range []string{
		"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-fable-5-1", "claude-fable-5",
		"claude-sonnet-5-5", "claude-haiku-5-5", "claude-haiku-4-5",
	} {
		if model == p || strings.HasPrefix(model, p+"-") {
			return p
		}
	}
	return "other"
}

// AnthropicProvider calls the Anthropic Messages API through the official SDK.
type AnthropicProvider struct {
	model    string
	client   anthropic.Client
	operator bool
	observe  OperatorObserver
	// forceEffort sends output_config.effort for a model modelFeatures does not list (model evaluation only).
	forceEffort bool
	// perRoute picks the model per call from operatorRouteModels (the operator key's default setup).
	perRoute bool
}

// AnthropicConfig configures the provider.
type AnthropicConfig struct {
	APIKey  string // required
	Model   string // default claude-haiku-4-5-20251001
	BaseURL string // tests only; empty uses the SDK default
	// Operator marks the key as the operator's: calls need a reserved budget
	// unit, the system prompt is cached, and Observe sees every hop's usage.
	Operator bool
	Observe  OperatorObserver
	// ForceEffort sends the route's effort even for a model outside the modelFeatures allowlist. For the
	// `rivolt ai-eval` side-by-side run only: a model that rejects the parameter answers 400.
	ForceEffort bool
	// PerRouteModels runs each route on OperatorModelFor(route) instead of Model. Operator key only.
	PerRouteModels bool
}

// NewAnthropic constructs a provider. Returns an error if no API key is set.
func NewAnthropic(cfg AnthropicConfig) (*AnthropicProvider, error) {
	// A key from a mounted secret or a paste often ends in a newline, which Go refuses in a header.
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("anthropic: api key is required")
	}
	if cfg.Model == "" {
		cfg.Model = "claude-haiku-4-5-20251001"
	}
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		// The SDK retries 408/409/429/5xx and connection errors with backoff;
		// other 4xx fail at once. No retry loop of ours on top.
		option.WithMaxRetries(2),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &AnthropicProvider{
		model:    cfg.Model,
		client:   anthropic.NewClient(opts...),
		operator: cfg.Operator,
		observe:  cfg.Observe,

		forceEffort: cfg.ForceEffort,
		perRoute:    cfg.Operator && cfg.PerRouteModels,
	}, nil
}

// Name reports a stable identifier for cache keying.
func (p *AnthropicProvider) Name() string { return "anthropic:" + p.model }

// NameForRoute is Name for the model a call on this route runs on.
func (p *AnthropicProvider) NameForRoute(r Route) string { return "anthropic:" + p.modelFor(r) }

// ModelSummary is what Settings shows as the model in use: the per-route models for the
// operator key, so users are not told the Opus default, and the plain name otherwise.
func (p *AnthropicProvider) ModelSummary() string {
	if !p.perRoute {
		return p.Name()
	}
	var parts []string
	for _, g := range []struct {
		label  string
		routes []Route
	}{{"recaps", []Route{RouteRecap}}, {"efficiency and advice", []Route{RouteEfficiency, RouteAdvice}}} {
		parts = append(parts, OperatorModelFor(g.routes[0])+" for "+g.label)
	}
	return "anthropic: " + strings.Join(parts, ", ")
}

func (p *AnthropicProvider) modelFor(r Route) string {
	if p.perRoute {
		return OperatorModelFor(r)
	}
	return p.model
}

// Complete sends a system+user prompt pair and returns the assistant text
// along with real token usage from the response.
func (p *AnthropicProvider) Complete(ctx context.Context, system, user string) (string, TokenUsage, error) {
	if !p.operator {
		return p.complete(ctx, system, user)
	}
	if !Reserved(ctx) {
		return "", TokenUsage{}, ErrOperatorDenied
	}
	taste, ok := TasteFrom(ctx)
	if !ok {
		return "", TokenUsage{}, ErrOperatorDenied
	}
	if err := taste.Take(ctx); err != nil {
		return "", TokenUsage{}, err
	}
	out, usage, err := p.complete(ctx, system, user)
	if err != nil && !errors.Is(err, ErrRefused) && !errors.Is(err, ErrTruncated) {
		taste.Refund(context.WithoutCancel(ctx)) // the provider failed: no answer was produced
	}
	return out, usage, err
}

func (p *AnthropicProvider) complete(ctx context.Context, system, user string) (string, TokenUsage, error) {
	route := RouteFrom(ctx)
	rp, ok := routeTable[route]
	if !ok {
		route, rp = RouteOther, routeTable[RouteOther]
	}
	ctx, cancel := context.WithTimeout(ctx, rp.total)
	defer cancel()

	model := p.modelFor(route)
	useEffort, useFallback := modelFeatures(model)
	useEffort = useEffort || p.forceEffort
	params := anthropic.BetaMessageNewParams{
		Model:     model,
		MaxTokens: rp.maxTokens,
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user)),
		},
	}
	if system != "" {
		blk := anthropic.BetaTextBlockParam{Text: system}
		// Cache only where the prefix is shared across users: the operator
		// key. On a single user's own key a write the user never reads back
		// within the TTL would only cost them 1.25x.
		if p.operator {
			blk.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
		}
		params.System = []anthropic.BetaTextBlockParam{blk}
	}
	if useEffort {
		params.OutputConfig = anthropic.BetaOutputConfigParam{Effort: rp.effort}
	}
	if useFallback {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}

	msg, err := p.client.Beta.Messages.New(ctx, params, option.WithRequestTimeout(rp.attempt))
	if err != nil {
		return "", TokenUsage{}, describeAPIError(err)
	}

	usage := p.account(ctx, route, msg)

	// Stop reason first: a refusal or a cut-off answer has nothing to read.
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		slog.WarnContext(ctx, "anthropic: request refused", "route", string(route),
			"model", ServedModelLabel(string(msg.Model)), "category", string(msg.StopDetails.Category))
		return "", usage, ErrRefused
	case anthropic.BetaStopReasonMaxTokens:
		return "", usage, ErrTruncated
	}

	// Text blocks only: Opus 5.5 also returns thinking blocks.
	var out strings.Builder
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			out.WriteString(t.Text)
		}
	}
	if out.Len() == 0 {
		return "", usage, fmt.Errorf("anthropic: empty response")
	}
	return out.String(), usage, nil
}

// account sums the response's usage and, for the operator key, reports each
// model hop (a fallback is a second hop on another model) to the observer.
// The caller's TokenUsage counts the whole prompt, cached or not.
func (p *AnthropicProvider) account(ctx context.Context, route Route, msg *anthropic.BetaMessage) TokenUsage {
	hops := make([]OperatorUsage, 0, 2)
	for _, it := range msg.Usage.Iterations {
		if it.Type != "message" && it.Type != "fallback_message" {
			continue
		}
		model := string(it.Model)
		if model == "" {
			model = string(msg.Model)
		}
		hops = append(hops, OperatorUsage{
			Route: route, Model: ServedModelLabel(model),
			Input: it.InputTokens, Output: it.OutputTokens,
			CacheRead: it.CacheReadInputTokens, CacheWrite: it.CacheCreationInputTokens,
		})
	}
	if len(hops) == 0 {
		hops = append(hops, OperatorUsage{
			Route: route, Model: ServedModelLabel(string(msg.Model)),
			Input: msg.Usage.InputTokens, Output: msg.Usage.OutputTokens,
			CacheRead: msg.Usage.CacheReadInputTokens, CacheWrite: msg.Usage.CacheCreationInputTokens,
		})
	}
	var tu TokenUsage
	for _, h := range hops {
		tu.InputTokens += h.Input + h.CacheRead + h.CacheWrite
		tu.OutputTokens += h.Output
		if p.operator && p.observe != nil {
			p.observe(h)
		}
	}
	if p.operator {
		if taste, ok := TasteFrom(ctx); ok {
			var c float64
			for _, h := range hops {
				c += OperatorCostUSD(h)
			}
			taste.Spent(ctx, c)
		}
		slog.DebugContext(ctx, "anthropic usage", "route", string(route), "hops", len(hops),
			"cost_usd", func() (c float64) {
				for _, h := range hops {
					c += OperatorCostUSD(h)
				}
				return
			}(),
			"cache_read_input_tokens", msg.Usage.CacheReadInputTokens,
			"cache_creation_input_tokens", msg.Usage.CacheCreationInputTokens)
	}
	return tu
}

// describeAPIError turns an SDK error into one that names the status and error
// type but never carries the request, the key or the response body.
func describeAPIError(err error) error {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		return fmt.Errorf("anthropic: http %d (%s)", apierr.StatusCode, apierr.Type())
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("anthropic: %w", uerr.Err) // the typed URL stays out
	}
	return errors.New("anthropic: request failed")
}

// OperatorCostUSD prices one model hop at the model that served it: cache
// reads at 0.1x and cache writes at 1.25x of that model's input rate.
func OperatorCostUSD(u OperatorUsage) float64 {
	in, out, known := lookupPrice("anthropic", u.Model)
	if !known {
		// An unknown (or "other"-labelled) model is priced at the highest
		// Anthropic rate, so operator spend is never underestimated.
		in, out = maxAnthropicPrice()
	}
	return (float64(u.Input)*in + float64(u.CacheRead)*in*0.1 + float64(u.CacheWrite)*in*1.25 + float64(u.Output)*out) / 1_000_000.0
}

func maxAnthropicPrice() (in, out float64) {
	for k, p := range pricePer1MTokens {
		if strings.HasPrefix(k, "anthropic:") {
			in, out = max(in, p.In), max(out, p.Out)
		}
	}
	return
}
