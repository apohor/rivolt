package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/ai"
)

type evalFakeProvider struct{ prompts *[]string }

func (evalFakeProvider) Name() string { return "fake" }
func (p evalFakeProvider) Complete(_ context.Context, system, user string) (string, ai.TokenUsage, error) {
	*p.prompts = append(*p.prompts, system+"\n"+user)
	return `{"ok":true}`, ai.TokenUsage{InputTokens: 10, OutputTokens: 5}, nil
}

// Every built-in input must build a prompt on all three routes, five of each.
func TestAIEvalCasesBuildPrompts(t *testing.T) {
	cases, err := aiEvalCases(5)
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	a := ai.NewAnalyzer(evalFakeProvider{&prompts})
	routes := map[ai.Route]int{}
	for _, c := range cases {
		routes[c.route]++
		if _, _, err := c.call(ai.WithUnmetered(context.Background()), a); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	for _, r := range []ai.Route{ai.RouteRecap, ai.RouteEfficiency, ai.RouteAdvice} {
		if routes[r] != 5 {
			t.Errorf("route %s: %d inputs, want 5", r, routes[r])
		}
	}
	if len(prompts) != 15 {
		t.Fatalf("%d prompts", len(prompts))
	}
}

func TestAIEvalNeedsKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	var out, errOut bytes.Buffer
	if code := runAIEval(nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "ANTHROPIC_API_KEY") || out.Len() != 0 {
		t.Fatalf("code %d out %q err %q", code, out.String(), errOut.String())
	}
}

func TestServedCostUsesServedModel(t *testing.T) {
	u := ai.TokenUsage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	haiku := ai.ComputeCost("anthropic", "claude-haiku-5-5", u.InputTokens, u.OutputTokens)
	opus := ai.ComputeCost("anthropic", "claude-opus-5-5", u.InputTokens, u.OutputTokens)
	if haiku >= opus {
		t.Fatalf("test needs haiku < opus, got %v %v", haiku, opus)
	}
	if got := servedCost("claude-haiku-5-5", []string{"claude-opus-5-5"}, u); got != opus {
		t.Errorf("served opus priced as %v, want %v", got, opus)
	}
	if got := servedCost("claude-haiku-5-5", nil, u); got != haiku {
		t.Errorf("no report priced as %v, want requested %v", got, haiku)
	}
	if got := servedCost("claude-haiku-5-5", []string{"claude-haiku-5-5", "claude-opus-5-5"}, u); got != opus {
		t.Errorf("mixed priced as %v, want dearest %v", got, opus)
	}
	if got := servedCost("claude-opus-5-5", []string{"other"}, u); got < opus {
		t.Errorf("unknown served id priced %v, below the requested %v", got, opus)
	}
}
