package api

import (
	"net/http"
	"os"
	"strings"

	"github.com/apohor/rivolt/internal/rivian"
)

// rivianActionsEnabled reports whether this host can talk to Rivian: false
// when the client is the stub or the replay (the admin site and the demo never
// reach Rivian), or when RIVOLT_RIVIAN_LIVE_FORBIDDEN is set. It is what
// /api/config advertises as features.rivian_actions, so the SPA hides the
// buttons that could only fail, and what the admin routes that call Rivian
// check before doing anything.
func rivianActionsEnabled() bool {
	if rivian.ParseBoolEnv(os.Getenv("RIVOLT_RIVIAN_LIVE_FORBIDDEN")) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RIVIAN_CLIENT"))) {
	case "stub", "replay":
		return false
	}
	return true
}

// requireRivianActions answers 409 and reports false when this host has no
// Rivian access, so a call that can only fail says why instead of failing
// obscurely.
func requireRivianActions(w http.ResponseWriter) bool {
	if rivianActionsEnabled() {
		return true
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": "Rivian is disabled on this host"})
	return false
}
