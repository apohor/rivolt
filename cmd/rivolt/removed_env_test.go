package main

import (
	"errors"
	"strings"
	"testing"
)

func TestHeaderSignInSettingRefusesToStart(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "RIVOLT_TRUSTED_PROXY_CIDR" {
				return v
			}
			return ""
		}
	}
	for _, v := range []string{"10.0.0.0/8", "10.42.1.7/32", " 127.0.0.1/32 ", "not-a-cidr"} {
		if err := removedSettingError(env(v)); !errors.Is(err, errHeaderSignInRemoved) {
			t.Errorf("RIVOLT_TRUSTED_PROXY_CIDR=%q: %v, want the removal error", v, err)
		}
	}
	for _, v := range []string{"", "   "} {
		if err := removedSettingError(env(v)); err != nil {
			t.Errorf("RIVOLT_TRUSTED_PROXY_CIDR=%q (blank) refused: %v", v, err)
		}
	}
	if got := errHeaderSignInRemoved.Error(); got == "" || !strings.Contains(got, "header sign-in was removed; use OIDC") {
		t.Errorf("the message must say what to do: %q", got)
	}
}
