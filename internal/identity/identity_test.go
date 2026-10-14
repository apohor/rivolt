package identity

import (
	"testing"

	"github.com/apohor/rivolt/internal/kratos"
)

func TestTrustFirstPartyEmailsDefaultsOn(t *testing.T) {
	for v, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "FALSE": false, " false ": false} {
		t.Setenv("RIVOLT_TRUST_FIRST_PARTY_EMAILS", v)
		if got := TrustFirstPartyEmails(); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

func TestTrustUnverifiedOIDCEmailDefaultsOff(t *testing.T) {
	for v, want := range map[string]bool{"": false, "false": false, "true": true, "TRUE": true} {
		t.Setenv("RIVOLT_OIDC_TRUST_UNVERIFIED_EMAIL", v)
		if got := TrustUnverifiedOIDCEmail(); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

func TestFirstPartyEmailVerified_OneAnswerForBothPaths(t *testing.T) {
	unverified := kratos.Identity{Traits: kratos.IdentityTraits{Email: "a@example.com"}}
	verified := kratos.Identity{
		Traits:              kratos.IdentityTraits{Email: "a@example.com"},
		VerifiableAddresses: []kratos.VerifiableAddress{{Value: "a@example.com", Verified: true}},
	}
	// Default (prod): Kratos verification is off, first-party emails count.
	t.Setenv("RIVOLT_TRUST_FIRST_PARTY_EMAILS", "")
	if !FirstPartyEmailVerified(unverified) || !FirstPartyEmailVerified(verified) {
		t.Fatal("default must trust first-party emails")
	}
	// Open-registration self-hoster: only Kratos's own state counts.
	t.Setenv("RIVOLT_TRUST_FIRST_PARTY_EMAILS", "false")
	if FirstPartyEmailVerified(unverified) {
		t.Fatal("unverified Kratos email must not count when trust is off")
	}
	if !FirstPartyEmailVerified(verified) {
		t.Fatal("verified Kratos email must count")
	}
}
