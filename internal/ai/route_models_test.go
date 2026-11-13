package ai

import (
	"context"
	"net/http/httptest"
	"testing"
)

// The owner-approved mapping (model evaluation): do not change it without a new ruling.
func TestOperatorRouteModelsAreFixed(t *testing.T) {
	for r, want := range map[Route]string{
		RouteRecap:      "claude-haiku-5-5",
		RouteEfficiency: "claude-sonnet-5-5",
		RouteAdvice:     "claude-sonnet-5-5",
		RoutePing:       OperatorModel,
		RouteOther:      OperatorModel,
	} {
		if got := OperatorModelFor(r); got != want {
			t.Errorf("%s runs on %s, want %s", r, got, want)
		}
	}
	if OperatorModel != "claude-opus-5-5" {
		t.Errorf("OperatorModel = %s", OperatorModel)
	}
}

func TestPerRouteProviderSendsTheRoutesModelAndEffort(t *testing.T) {
	for route, want := range map[Route]struct{ model, effort string }{
		RouteRecap:      {"claude-haiku-5-5", "low"},
		RouteEfficiency: {"claude-sonnet-5-5", "medium"},
		RouteAdvice:     {"claude-sonnet-5-5", "medium"},
		RouteOther:      {"claude-opus-5-5", "medium"},
	} {
		f := &fakeAnthropic{replies: []fakeReply{ok(want.model, "end_turn", textAndThinking, `{"input_tokens":1,"output_tokens":1}`)}}
		srv := httptest.NewServer(f)
		p, err := NewAnthropic(AnthropicConfig{APIKey: "sk-test", Model: OperatorModel, BaseURL: srv.URL, Operator: true, PerRouteModels: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.Complete(WithUnmetered(WithRoute(context.Background(), route)), "s", "u"); err != nil {
			t.Fatalf("%s: %v", route, err)
		}
		srv.Close()
		b := f.bodies[0]
		if b["model"] != want.model {
			t.Errorf("%s: model %v, want %s", route, b["model"], want.model)
		}
		if eff := b["output_config"].(map[string]any)["effort"]; eff != want.effort {
			t.Errorf("%s: effort %v, want %s", route, eff, want.effort)
		}
		if _, has := b["fallbacks"]; has != (want.model == OperatorModel) {
			t.Errorf("%s: fallbacks present = %v", route, has)
		}
		if got := NewAnalyzer(p).ModelNameFor(route); got != "anthropic:"+want.model {
			t.Errorf("%s: ModelNameFor = %s", route, got)
		}
	}
}

// A user's own key keeps the model they chose on every route.
func TestOwnKeyIgnoresRouteModels(t *testing.T) {
	p, err := NewAnthropic(AnthropicConfig{APIKey: "sk-own", Model: "claude-haiku-4-5-20251001", PerRouteModels: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := NewAnalyzer(p).ModelNameFor(RouteEfficiency); got != "anthropic:claude-haiku-4-5-20251001" {
		t.Errorf("own key on efficiency: %s", got)
	}
}
