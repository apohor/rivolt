package settings

import (
	"context"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/ai"
)

func TestBuildProviderOperatorKey(t *testing.T) {
	name := func(cfg AIConfig, op string) string {
		t.Helper()
		p, err := buildProvider(cfg, op, nil)
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			return ""
		}
		return p.Name()
	}
	op := "anthropic:" + ai.OperatorModel

	// No key of the install's own: the operator key answers, on its model.
	if got := name(AIConfig{}, "sk-op"); got != op {
		t.Errorf("auto, no own key: %q, want %q", got, op)
	}
	if got := name(AIConfig{Provider: ProviderAnthropic}, "sk-op"); got != op {
		t.Errorf("anthropic chosen, no own key: %q, want %q", got, op)
	}
	// A user's own key always wins and keeps its own model.
	if got := name(AIConfig{AnthropicKey: "sk-own", AnthropicModel: "claude-haiku-4-5-20251001"}, "sk-op"); got != "anthropic:claude-haiku-4-5-20251001" {
		t.Errorf("own anthropic key: %q", got)
	}
	if got := name(AIConfig{OpenAIKey: "k", OpenAIModel: "gpt-x"}, "sk-op"); strings.Contains(got, ai.OperatorModel) {
		t.Errorf("own OpenAI key must not fall to the operator: %q", got)
	}
	// No operator key: unchanged behaviour.
	if got := name(AIConfig{}, ""); got != "" {
		t.Errorf("nothing configured: %q, want disabled", got)
	}
	if _, err := buildProvider(AIConfig{Provider: ProviderAnthropic}, "", nil); err == nil {
		t.Error("anthropic chosen with no key at all must still error")
	}
}

type memKV map[string]string

func (m memKV) GetAll(context.Context) (map[string]string, error) { return m, nil }
func (m memKV) Set(_ context.Context, k, v string) error          { m[k] = v; return nil }
func (m memKV) Delete(_ context.Context, k string) error          { delete(m, k); return nil }

// The operator key lives outside AIConfig: saving settings never writes it.
func TestOperatorKeyIsNeverPersisted(t *testing.T) {
	kv := memKV{}
	m, err := NewManager(context.Background(), kv, AIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	m.SetOperator("sk-op-secret", nil)
	if m.Provider() == nil || m.Provider().Name() != "anthropic:"+ai.OperatorModel {
		t.Fatalf("provider = %v", m.Provider())
	}
	model := "claude-haiku-4-5-20251001"
	if _, err := m.Update(context.Background(), AIUpdate{AnthropicModel: &model}); err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		if strings.Contains(v, "sk-op-secret") || strings.Contains(k, "sk-op-secret") {
			t.Fatalf("operator key persisted under %q", k)
		}
	}
	m.SetOperator("", nil)
	if m.Provider() != nil {
		t.Error("removing the operator key must disable AI again")
	}
}
