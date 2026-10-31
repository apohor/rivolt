package main

import (
	"errors"
	"strings"
)

// errHeaderSignInRemoved is why the server will not start while RIVOLT_TRUSTED_PROXY_CIDR is set. Trusting a user header from a network
// range let anything inside the range sign in as anyone (N48), so the feature was removed rather than fixed; a silent ignore would leave
// an operator believing their proxy still authenticates their users.
var errHeaderSignInRemoved = errors.New("RIVOLT_TRUSTED_PROXY_CIDR is set, but header sign-in was removed; use OIDC (RIVOLT_OIDC_PROVIDERS) and unset it")

// removedSettingError reports a setting that no longer exists and must not be silently ignored. An empty value (docker-compose passes the
// variable through, blank) is not a setting.
func removedSettingError(getenv func(string) string) error {
	if strings.TrimSpace(getenv("RIVOLT_TRUSTED_PROXY_CIDR")) != "" {
		return errHeaderSignInRemoved
	}
	return nil
}
