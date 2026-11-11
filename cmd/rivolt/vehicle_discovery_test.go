package main

import "testing"

func TestOwnerDiscoveryNeedsGroupLeadAndForward(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for name, tc := range map[string]struct {
		env map[string]string
		ok  bool
	}{
		"both on":      {map[string]string{"RIVOLT_GROUP_LEAD": "1", "RIVOLT_FORWARD": "1"}, true},
		"no group":     {map[string]string{"RIVOLT_FORWARD": "1"}, false},
		"no forward":   {map[string]string{"RIVOLT_GROUP_LEAD": "1"}, false},
		"neither":      {nil, false},
		"lead is true": {map[string]string{"RIVOLT_GROUP_LEAD": "true", "RIVOLT_FORWARD": "1"}, false},
	} {
		if err := ownerDiscoveryEnvError(env(tc.env)); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v", name, err, tc.ok)
		}
	}
}

func TestPermitEnforceNeedsOwnerDiscovery(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for name, tc := range map[string]struct {
		env map[string]string
		ok  bool
	}{
		"enforce with discovery":    {map[string]string{"RIVOLT_RIVIAN_PERMIT": "enforce", "RIVOLT_OWNER_DISCOVERY": "1"}, true},
		"enforce without discovery": {map[string]string{"RIVOLT_RIVIAN_PERMIT": "enforce"}, false},
		"observe without discovery": {map[string]string{"RIVOLT_RIVIAN_PERMIT": "observe"}, true},
		"off":                       {nil, true},
	} {
		if err := permitEnforceEnvError(env(tc.env)); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v", name, err, tc.ok)
		}
	}
}
