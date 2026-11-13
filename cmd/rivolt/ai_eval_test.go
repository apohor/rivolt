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

// Every built-in input must build a prompt on all three routes, five of each, and carry no real place name.
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
		if _, _, err := c.call(ai.WithReservation(context.Background()), a); err != nil {
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
