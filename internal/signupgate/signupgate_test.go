package signupgate

import "testing"

func TestDisabledDefaultsOff(t *testing.T) {
	for v, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "TRUE": true, " true ": true} {
		t.Setenv("RIVOLT_SIGNUP_DISABLED", v)
		if got := Disabled(); got != want {
			t.Errorf("%q: Disabled = %v, want %v", v, got, want)
		}
	}
}
