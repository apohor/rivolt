package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAnthropic serves canned /v1/messages replies in order and records the
// requests it saw.
type fakeAnthropic struct {
	mu      sync.Mutex
	replies []fakeReply
	bodies  []map[string]any
	headers []http.Header
}

type fakeReply struct {
	status int
	body   string
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	idx := len(f.bodies)
	f.bodies = append(f.bodies, body)
	f.headers = append(f.headers, r.Header.Clone())
	if idx >= len(f.replies) {
		idx = len(f.replies) - 1
	}
	rep := f.replies[idx]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	_, _ = w.Write([]byte(rep.body))
}

func (f *fakeAnthropic) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func ok(model, stop, content, usage string) fakeReply {
	return fakeReply{200, `{"id":"msg_1","type":"message","role":"assistant","model":"` + model +
		`","content":` + content + `,"stop_reason":"` + stop + `","usage":` + usage + `}`}
}

const textAndThinking = `[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"hello "},{"type":"text","text":"world"}]`

func newTest(t *testing.T, f *fakeAnthropic, model string, operator bool, obs OperatorObserver) *AnthropicProvider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p, err := NewAnthropic(AnthropicConfig{APIKey: "sk-test-secret", Model: model, BaseURL: srv.URL, Operator: operator, Observe: obs})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOperatorRequestShapeAndTextOnly(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{ok("claude-opus-5-5", "end_turn", textAndThinking, `{"input_tokens":10,"output_tokens":20}`)}}
	p := newTest(t, f, OperatorModel, true, nil)
	ctx := WithReservation(WithRoute(context.Background(), RouteRecap))
	got, usage, err := p.Complete(ctx, "sys prompt", "user text")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Fatalf("text = %q, want only the text blocks", got)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 20 {
		t.Fatalf("usage = %+v", usage)
	}
	b := f.bodies[0]
	for _, k := range []string{"thinking", "temperature", "top_p", "top_k"} {
		if _, has := b[k]; has {
			t.Errorf("request carries %q", k)
		}
	}
	if b["model"] != "claude-opus-5-5" || b["max_tokens"].(float64) != 4096 {
		t.Errorf("model/max_tokens = %v/%v", b["model"], b["max_tokens"])
	}
	if eff := b["output_config"].(map[string]any)["effort"]; eff != "low" {
		t.Errorf("recap effort = %v, want low", eff)
	}
	if b["fallbacks"] != "default" {
		t.Errorf("fallbacks = %v", b["fallbacks"])
	}
	if !strings.Contains(f.headers[0].Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("anthropic-beta = %q", f.headers[0].Get("anthropic-beta"))
	}
	sys := b["system"].([]any)[0].(map[string]any)
	if sys["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Errorf("operator system block is not cached: %v", sys)
	}
}

func TestEffortPerRoute(t *testing.T) {
	for route, want := range map[Route]string{RouteEfficiency: "medium", RouteAdvice: "medium", RoutePing: "low", RouteOther: "medium"} {
		f := &fakeAnthropic{replies: []fakeReply{ok("claude-opus-5-5", "end_turn", textAndThinking, `{"input_tokens":1,"output_tokens":1}`)}}
		p := newTest(t, f, OperatorModel, true, nil)
		if _, _, err := p.Complete(WithReservation(WithRoute(context.Background(), route)), "s", "u"); err != nil {
			t.Fatal(err)
		}
		if got := f.bodies[0]["output_config"].(map[string]any)["effort"]; got != want {
			t.Errorf("%s effort = %v, want %s", route, got, want)
		}
	}
	if routeTable[RoutePing].maxTokens < 2048 {
		t.Error("ping max_tokens is too low for always-on thinking")
	}
}

func TestOperatorWithoutReservationIsRefusedBeforeAnyRequest(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{ok("claude-opus-5-5", "end_turn", textAndThinking, `{}`)}}
	p := newTest(t, f, OperatorModel, true, nil)
	if _, _, err := p.Complete(context.Background(), "s", "u"); err != ErrOperatorDenied {
		t.Fatalf("err = %v, want ErrOperatorDenied", err)
	}
	if f.calls() != 0 {
		t.Fatal("request was sent without a reservation")
	}
}

func TestOwnKeyKeepsPlainRequestOnOlderModel(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{ok("claude-haiku-4-5-20251001", "end_turn", `[{"type":"text","text":"hi"}]`, `{"input_tokens":3,"output_tokens":4}`)}}
	var observed int
	p := newTest(t, f, "claude-haiku-4-5-20251001", false, func(OperatorUsage) { observed++ })
	if _, _, err := p.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	b := f.bodies[0]
	for _, k := range []string{"output_config", "fallbacks", "thinking"} {
		if _, has := b[k]; has {
			t.Errorf("older model request carries %q", k)
		}
	}
	if h := f.headers[0].Get("anthropic-beta"); h != "" {
		t.Errorf("anthropic-beta = %q on an older model", h)
	}
	if sys := b["system"].([]any)[0].(map[string]any); sys["cache_control"] != nil {
		t.Errorf("a user's own key must not pay for cache writes: %v", sys)
	}
	if observed != 0 {
		t.Error("own-key usage reached the operator counter")
	}
}

func TestRefusalIsCleanAndNotRetried(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{ok("claude-opus-5-5", "refusal", `[]`, `{"input_tokens":5,"output_tokens":0}`)}}
	p := newTest(t, f, OperatorModel, true, nil)
	_, _, err := p.Complete(WithReservation(context.Background()), "s", "u")
	if err != ErrRefused {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if f.calls() != 1 {
		t.Fatalf("calls = %d, want 1", f.calls())
	}
}

func TestTruncationIsAnError(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{ok("claude-opus-5-5", "max_tokens", `[{"type":"text","text":"{\"a\":"}]`, `{"input_tokens":5,"output_tokens":99}`)}}
	p := newTest(t, f, OperatorModel, true, nil)
	if _, _, err := p.Complete(WithReservation(context.Background()), "s", "u"); err != ErrTruncated {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
}

func TestClientErrorNotRetriedAndLeaksNothing(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{{400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad sk-test-secret"}}`}}}
	p := newTest(t, f, OperatorModel, true, nil)
	_, _, err := p.Complete(WithReservation(context.Background()), "s", "u")
	if err == nil {
		t.Fatal("want error")
	}
	if f.calls() != 1 {
		t.Fatalf("a 400 was retried: %d calls", f.calls())
	}
	if !strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "sk-test-secret") {
		t.Fatalf("error = %q", err)
	}
}

func TestServerErrorRetriedBySDK(t *testing.T) {
	f := &fakeAnthropic{replies: []fakeReply{
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`},
		ok("claude-opus-5-5", "end_turn", `[{"type":"text","text":"fine"}]`, `{"input_tokens":1,"output_tokens":1}`),
	}}
	p := newTest(t, f, OperatorModel, true, nil)
	got, _, err := p.Complete(WithReservation(context.Background()), "s", "u")
	if err != nil || got != "fine" {
		t.Fatalf("got %q, %v", got, err)
	}
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
}

func TestOperatorUsageCountsEveryHopAndCacheReads(t *testing.T) {
	first := ok("claude-opus-5-5", "end_turn", textAndThinking,
		`{"input_tokens":20,"output_tokens":30,"cache_creation_input_tokens":600,"cache_read_input_tokens":0}`)
	second := ok("claude-opus-5-5", "end_turn", textAndThinking,
		`{"input_tokens":20,"output_tokens":30,"cache_creation_input_tokens":0,"cache_read_input_tokens":600}`)
	hops := ok("claude-opus-5-5", "end_turn", textAndThinking,
		`{"input_tokens":5,"output_tokens":7,"iterations":[`+
			`{"type":"message","model":"claude-opus-5-5","input_tokens":5,"output_tokens":0},`+
			`{"type":"fallback_message","model":"claude-opus-4-8","input_tokens":5,"output_tokens":7}]}`)
	f := &fakeAnthropic{replies: []fakeReply{first, second, hops}}
	var seen []OperatorUsage
	p := newTest(t, f, OperatorModel, true, func(u OperatorUsage) { seen = append(seen, u) })
	ctx := WithReservation(WithRoute(context.Background(), RouteAdvice))
	for i := 0; i < 3; i++ {
		if _, _, err := p.Complete(ctx, "stable system prompt", "u"); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("observed %d hops, want 4: %+v", len(seen), seen)
	}
	if seen[0].CacheWrite != 600 || seen[0].CacheRead != 0 {
		t.Errorf("first call = %+v, want a cache write", seen[0])
	}
	if seen[1].CacheRead != 600 {
		t.Errorf("second call cache_read = %d, want 600", seen[1].CacheRead)
	}
	if seen[2].Model != "claude-opus-5-5" || seen[3].Model != "claude-opus-4-8" || seen[3].Output != 7 {
		t.Errorf("hops = %+v / %+v", seen[2], seen[3])
	}
	for _, u := range seen {
		if u.Route != RouteAdvice {
			t.Errorf("route = %q", u.Route)
		}
	}
}

func TestServedModelLabelIsAFixedSet(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5": "claude-opus-5-5", "claude-opus-5": "claude-opus-5", "claude-opus-4-8": "claude-opus-4-8",
		"claude-haiku-4-5-20251001": "claude-haiku-4-5", "totally-new-model": "other", "": "other",
	} {
		if got := ServedModelLabel(in); got != want {
			t.Errorf("ServedModelLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOperatorPrices(t *testing.T) {
	if got := ComputeCost("anthropic", "claude-opus-5-5", 1_000_000, 1_000_000); got != 24 {
		t.Errorf("opus-5-5 cost = %v, want 24", got)
	}
	if got := ComputeCost("anthropic", "claude-opus-4-8", 1_000_000, 1_000_000); got != 30 {
		t.Errorf("opus-4-8 cost = %v, want 30", got)
	}
}

func TestOperatorCostPricesEachHopAtItsServingModel(t *testing.T) {
	// 1M uncached in + 1M out on opus-5-5 (4/20) = 24; the same on opus-4-8 (5/25) = 30.
	if got := OperatorCostUSD(OperatorUsage{Model: "claude-opus-5-5", Input: 1e6, Output: 1e6}); got != 24 {
		t.Errorf("opus-5-5 = %v, want 24", got)
	}
	if got := OperatorCostUSD(OperatorUsage{Model: "claude-opus-4-8", Input: 1e6, Output: 1e6}); got != 30 {
		t.Errorf("opus-4-8 = %v, want 30", got)
	}
	// Cache read 0.1x and write 1.25x of the 4/M input rate.
	if got := OperatorCostUSD(OperatorUsage{Model: "claude-opus-5-5", CacheRead: 1e6, CacheWrite: 1e6}); got != 0.4+5 {
		t.Errorf("cache = %v, want 5.4", got)
	}
}

func TestOperatorCostUnknownModelUsesHighestRate(t *testing.T) {
	in, out := maxAnthropicPrice()
	want := (in + out) // 1M each
	if got := OperatorCostUSD(OperatorUsage{Model: "other", Input: 1e6, Output: 1e6}); got != want {
		t.Errorf("unknown = %v, want %v", got, want)
	}
	if in < 5 || out < 25 {
		t.Errorf("highest known rate = %v/%v, want at least the opus 5/25", in, out)
	}
}

func TestOperatorCostIgnoresDateSuffix(t *testing.T) {
	if got := OperatorCostUSD(OperatorUsage{Model: "claude-opus-5-5-20260301", Input: 1e6, Output: 1e6}); got != 24 {
		t.Errorf("dated opus-5-5 = %v, want 24", got)
	}
}
