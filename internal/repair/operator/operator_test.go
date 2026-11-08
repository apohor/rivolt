package operator

import (
	"context"
	"io"
	"strings"
	"testing"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

// The label is a role, never a name, and a reason is required, before anything is opened.
func TestOpenValidatesBeforeOpeningAnything(t *testing.T) {
	for name, tc := range map[string]struct {
		env  func(string) string
		want string
	}{
		"no role":           {env(EnvDSN, "postgres://nowhere/x", EnvReason, "r"), EnvRole},
		"a person's name":   {env(EnvDSN, "postgres://nowhere/x", EnvRole, "Alex", EnvReason, "r"), EnvRole},
		"no reason":         {env(EnvDSN, "postgres://nowhere/x", EnvRole, "lead"), EnvReason},
		"reason too long":   {env(EnvDSN, "postgres://nowhere/x", EnvRole, "owner", EnvReason, strings.Repeat("x", 301)), "300"},
		"role required, no": {env(EnvRequireRole, "1"), EnvRequireRole},
	} {
		_, err := Open(context.Background(), "repair-test", true, tc.env, "postgres://legacy/x", io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestValidateAcceptsTheTwoRoleLabels(t *testing.T) {
	for _, role := range []string{"lead", "owner"} {
		if err := validate(role, "reprice the 12 stretched rows"); err != nil {
			t.Errorf("%s: %v", role, err)
		}
	}
}
