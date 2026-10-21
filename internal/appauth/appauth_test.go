package appauth

import "testing"

// RFC 7636, Appendix B.
func TestChallengeMatchesTheRFCExample(t *testing.T) {
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge %q", got)
	}
}

func TestValidChallenge(t *testing.T) {
	for c, want := range map[string]bool{
		"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM":  true,
		"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-c":   false, // 42
		"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM=": false, // padding
		"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw+cM":  false, // not base64url
		"": false,
	} {
		if ValidChallenge(c) != want {
			t.Errorf("ValidChallenge(%q) != %v", c, want)
		}
	}
}
