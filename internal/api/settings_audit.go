package api

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/sessions"
)

// logSettingsWrite is the one audit line for a settings write that the iPhone app may make: who (the user id), from where (web or
// app), which group and which fields. Never a value, so nothing in it is a secret or a rate.
func logSettingsWrite(r *http.Request, group string, fields ...string) {
	source := "web"
	if ref, ok := auth.SessionFromContext(r.Context()); ok && ref.Kind == sessions.KindApp {
		source = "app"
	}
	uid, _ := auth.UserFromContext(r.Context())
	if uid == uuid.Nil {
		return
	}
	slog.InfoContext(r.Context(), "settings write", "user_id", uid.String(), "source", source, "group", group, "fields", fields)
}
