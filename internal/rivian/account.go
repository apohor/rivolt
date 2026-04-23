package rivian

import "context"

// Account is the auth-surface shared by every Rivian Client variant
// that can drive the Settings sign-in UI. Both *LiveClient and the
// *MockClient implement it, which lets RIVIAN_CLIENT=mock exercise
// exactly the same /api/settings/rivian flow the real deployment
// uses — you sign in, maybe clear an MFA prompt, then vehicles show
// up. The stub client deliberately does NOT implement this because
// there's nothing to sign into.
type Account interface {
	// Login accepts a first-leg (Email+Password) or second-leg
	// (OTP) set of credentials. Must return ErrMFARequired when the
	// server issued an MFA challenge.
	Login(ctx context.Context, c Credentials) error
	// Logout clears every authenticated-session field locally.
	Logout()
	// Authenticated reports whether a valid session exists.
	Authenticated() bool
	// MFAPending reports whether Login returned ErrMFARequired and
	// the client is waiting for an OTP. Survives page reloads.
	MFAPending() bool
	// Email returns the email the current session is authenticated
	// as, or "" if no session is active.
	Email() string
	// Snapshot returns a copy of the current session. Persisted as
	// JSON by settings.SaveRivianSession so a restart doesn't drop
	// the login.
	Snapshot() Session
	// Restore hydrates the client from a prior Snapshot. No I/O.
	Restore(s Session)
}

// Compile-time assertions keep the interface honest.
var (
	_ Account = (*LiveClient)(nil)
	_ Account = (*MockClient)(nil)
)
