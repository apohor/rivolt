package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
)

// RememberedRevoker forgets what the identity provider remembers about a user. Revoking Rivolt's own sessions is not a lock-out
// by itself: Hydra remembers an accepted login (RIVOLT_OIDC_HYDRA_REMEMBER_FOR, 24 h on prod) and answers the next login request
// from that browser with skip=true, so the browser is signed straight back in with no password. The people who revoke (sign out
// other devices, change or secure the account, support securing an account) mean it to be final.
type RememberedRevoker interface {
	RevokeRemembered(ctx context.Context, uid uuid.UUID) error
}

// IdentityRevoker clears, for one user: Hydra's remembered logins, its consent grants (which revokes the OAuth tokens issued under
// them), and the Kratos sessions (LoginByPassword creates one per sign-in). It finds the Kratos identity from the account's
// username, which is the email. An account with no Kratos identity (Google, GitHub or Apple only) has nothing remembered here.
type IdentityRevoker struct {
	Hydra  *hydra.Client
	Kratos *kratos.Client
	// Username is the account's username (db.RawUsernameByID on the system pool).
	Username func(ctx context.Context, uid uuid.UUID) (string, error)
}

// RevokeRemembered attempts all three steps and returns every error, so one failing does not leave the others undone.
func (r *IdentityRevoker) RevokeRemembered(ctx context.Context, uid uuid.UUID) error {
	if r == nil || !r.Kratos.Enabled() || r.Username == nil {
		return nil
	}
	username, err := r.Username(ctx, uid)
	if err != nil {
		return fmt.Errorf("look up the account: %w", err)
	}
	if username == "" {
		return nil
	}
	id, err := r.Kratos.IdentityIDByEmail(ctx, username)
	if err != nil {
		return fmt.Errorf("find the identity: %w", err)
	}
	if id == "" {
		return nil
	}
	var errs []error
	if r.Hydra.Enabled() {
		errs = append(errs, r.Hydra.RevokeLoginSessions(ctx, id), r.Hydra.RevokeConsentSessions(ctx, id))
	}
	errs = append(errs, r.Kratos.RevokeIdentitySessions(ctx, id))
	return errors.Join(errs...)
}
