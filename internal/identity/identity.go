// Package identity holds the two install-wide switches that decide how
// much an email address is trusted when it identifies an account.
package identity

import (
	"os"
	"strings"

	"github.com/apohor/rivolt/internal/kratos"
)

// TrustFirstPartyEmails reports RIVOLT_TRUST_FIRST_PARTY_EMAILS
// (default true). Rivolt's own Kratos/Hydra only holds identities
// created through Rivolt sign-up or by an admin, so its email counts as
// verified even when Kratos email verification is off (prod's setup).
// One switch governs every first-party path: the Hydra consent
// email_verified claim, the direct Kratos-session path, admin
// promotion and the signup-closed bootstrap exception. A self-hoster
// whose Kratos allows open registration sets it to false and then only
// Kratos's own verifiable_addresses state counts, everywhere.
func TrustFirstPartyEmails() bool {
	v := strings.TrimSpace(os.Getenv("RIVOLT_TRUST_FIRST_PARTY_EMAILS"))
	return v == "" || !strings.EqualFold(v, "false")
}

// TrustUnverifiedOIDCEmail reports RIVOLT_OIDC_TRUST_UNVERIFIED_EMAIL
// (default false). When true an email (or an email-shaped
// preferred_username) the IdP did not verify may be an account key, as
// it always could before. Unsafe with an IdP that lets anyone register
// any address; for IdPs that never send email_verified.
func TrustUnverifiedOIDCEmail() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("RIVOLT_OIDC_TRUST_UNVERIFIED_EMAIL")), "true")
}

// FirstPartyEmailVerified is the single answer both first-party paths
// use for a Kratos identity: the Hydra consent handler (the
// email_verified claim in the id_token) and the direct Kratos-session
// resolver (admin promotion and the signup-closed bootstrap
// exception). With TrustFirstPartyEmails it is true; otherwise it is
// Kratos's own verification state of the trait email.
func FirstPartyEmailVerified(id kratos.Identity) bool {
	return TrustFirstPartyEmails() || id.EmailVerified()
}
