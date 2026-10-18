package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/adminaudit"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// Every admin write is recorded in admin_audit (internal/adminaudit): who,
// what, on whom, when and from which address. The row is inserted BEFORE the
// handler runs and the write is refused if that insert fails, so no admin
// write can happen unrecorded; afterwards the row is finished with the
// outcome (a row left without one is a write whose result is unknown). Read-only
// admin views are not logged.

// auditSpec describes one audited route.
type auditSpec struct {
	action     string // "user.set_role", "flag.kill_switch", ...
	targetType string // "user", "flag", "settings", ...
	targetID   string // fixed target id (a flag or settings name), or ""
	idParam    string // URL parameter holding the target's id ("id"), or ""
	userTarget bool   // the target is a user and idParam / bodyUser is its id
	bodyUser   string // JSON body key holding a user id (impersonation stop)
	usernameID string // JSON body key holding a username to derive the user id from (user create)
	keys       []string
	// fields: record the NAMES of the body's keys (never values), for bodies
	// that may carry secrets (the AI settings hold API keys).
	fields bool
	// custom derives extra details from the body (counts, derived booleans).
	custom func(body map[string]any) map[string]any
	// before returns the value being replaced (flags), for before/after.
	before func() map[string]any
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// auditDetails builds the stored details from a request body.
func auditDetails(spec auditSpec, body map[string]any) map[string]any {
	d := map[string]any{}
	for _, k := range spec.keys {
		if v, ok := body[k]; ok {
			d[k] = v
		}
	}
	if spec.fields {
		names := make([]string, 0, len(body))
		for k := range body {
			names = append(names, k)
		}
		sort.Strings(names)
		d["fields"] = names
	}
	if spec.custom != nil {
		for k, v := range spec.custom(body) {
			d[k] = v
		}
	}
	if spec.before != nil {
		for k, v := range spec.before() {
			d["before_"+k] = v
		}
	}
	return d
}

// adminAudit returns the middleware for one route. With a nil store (no
// database) it is a no-op.
func adminAudit(store *adminaudit.Store) func(auditSpec) func(http.Handler) http.Handler {
	return func(spec auditSpec) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			if store == nil {
				return next
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actor, ok := auth.UserFromContext(r.Context())
				if !ok {
					next.ServeHTTP(w, r) // requireAdminMW has already refused anonymous callers
					return
				}
				var body map[string]any
				if r.Body != nil && r.Method != http.MethodGet {
					raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
					_ = r.Body.Close()
					r.Body = io.NopCloser(bytes.NewReader(raw))
					_ = json.Unmarshal(raw, &body)
				}
				e := adminaudit.Entry{
					Actor: actor, Action: spec.action, TargetType: spec.targetType, TargetID: spec.targetID,
					Details: auditDetails(spec, body), IP: clientAddr(r),
				}
				if spec.idParam != "" {
					e.TargetID = chi.URLParam(r, spec.idParam)
					if spec.userTarget {
						if u, err := uuid.Parse(e.TargetID); err == nil {
							e.TargetUser = u
						}
					}
				}
				if spec.bodyUser != "" {
					if s, _ := body[spec.bodyUser].(string); s != "" {
						if u, err := uuid.Parse(s); err == nil {
							e.TargetUser, e.TargetID = u, u.String()
						}
					}
				}
				if spec.usernameID != "" {
					if s, _ := body[spec.usernameID].(string); s != "" {
						e.TargetUser = db.UserIDFor(s) // the id only; the username is not stored
						e.TargetID = e.TargetUser.String()
					}
				}
				id, err := store.Begin(r.Context(), e)
				if err != nil {
					slog.ErrorContext(r.Context(), "admin audit: not recorded, change refused", "action", spec.action, "err", err.Error())
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "the audit log is unavailable, so the change was not made"})
					return
				}
				ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
				defer func() {
					status := ww.Status()
					if status == 0 {
						status = http.StatusOK
					}
					if rec := recover(); rec != nil { // finish the row, then let the panic through
						_ = finishAudit(store, id, http.StatusInternalServerError)
						panic(rec)
					}
					if err := finishAudit(store, id, status); err != nil {
						slog.WarnContext(r.Context(), "admin audit: could not record the outcome", "id", id, "err", err.Error())
					}
				}()
				next.ServeHTTP(ww, r)
			})
		}
	}
}

// finishAudit records the outcome without depending on the request context
// (the client may already be gone).
func finishAudit(store *adminaudit.Store, id int64, status int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return store.Finish(ctx, id, status)
}

// handleAdminAuditList — GET /api/admin/audit?limit=&before=&action=&actor=&target_user=
// Newest first; `next_before` is the cursor for the next page (null at the end).
func handleAdminAuditList(store *adminaudit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "audit log unavailable"})
			return
		}
		q := r.URL.Query()
		f := adminaudit.Filter{Action: q.Get("action")}
		if n, err := strconv.Atoi(q.Get("limit")); err == nil {
			f.Limit = n
		}
		if n, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && n > 0 {
			f.Before = n
		}
		for key, dst := range map[string]*uuid.UUID{"actor": &f.Actor, "target_user": &f.TargetUser} {
			if v := q.Get(key); v != "" {
				u, err := uuid.Parse(v)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid " + key})
					return
				}
				*dst = u
			}
		}
		rows, next, err := store.List(r.Context(), f)
		if err != nil {
			serverError(w, r, "admin audit list", err)
			return
		}
		if rows == nil {
			rows = []adminaudit.Row{}
		}
		var nextOut any
		if next > 0 {
			nextOut = next
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"items": rows, "next_before": nextOut})
	}
}

// handleImpersonationStop — POST /api/admin/impersonation/stop {target_id}.
// The audit middleware records it; there is nothing else to do server-side
// (impersonation is a per-request header). The SPA calls it when the admin
// leaves "view as".
func handleImpersonationStop() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
}
