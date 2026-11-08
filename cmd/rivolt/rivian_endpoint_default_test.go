//go:build !e2e_forward

package main

import "testing"

// The default build (what releases and images are) cannot read the endpoint override: setting the variable changes nothing.
func TestDefaultBuildIgnoresTheRivianEndpointOverride(t *testing.T) {
	for _, v := range []string{"http://127.0.0.1:9999", "https://evil.example/graphql", "http://[::1]:1"} {
		t.Setenv("RIVOLT_RIVIAN_ENDPOINT", v)
		if apply, err := rivianEndpointOverride(); apply != nil || err != nil {
			t.Errorf("with the variable set to %q the default build returned an override (%v), %v; want nothing and no error", v, apply != nil, err)
		}
	}
}
