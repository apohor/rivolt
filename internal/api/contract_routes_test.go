package api

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/changelog"
	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/passkeys"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/signuprequests"
	"github.com/apohor/rivolt/internal/statement"
	"github.com/apohor/rivolt/internal/trips"
)

// Contract check B (Sprint 11): the routes the clients call exist. The iPhone app and the web spell their API paths as string
// literals; this reads them out of the source and checks each (method, path) against the real router's routes (chi.Walk). A
// route renamed or removed on the server, or a typo in a client, fails Go CI instead of QA. It is a text match, not a type check,
// so the reviewed ignore list below carries the exceptions, each with its reason.

// clientCall is one API call spelled in client source.
type clientCall struct {
	file   string
	line   int
	method string
	path   string // normalised: no query, interpolations as {x}, no trailing slash
}

// ignoredClientCalls (key: "swift|web METHOD path") are literals that look like calls but are not served by a route of this router, each with why. An entry that
// matches no call in the source fails the test, so the list cannot rot.
var ignoredClientCalls = map[string]string{
	"swift GET /api":          "WidgetFetch.request checks that a path the widget was handed starts with \"/api/\"; it is a prefix test, not a call",
	"web GET /api/auth/hydra": "api.ts's 401 handler checks url.startsWith(\"/api/auth/hydra/\") to leave the signed-out Hydra sign-in alone; it is a prefix test, not a call",
}

// readInterpolated reads a string literal body from s starting at i (just after the opening quote) and returns the body with every
// interpolation replaced by {x}, and the index after the closing quote. open is how an interpolation starts and how to find its end.
func scanLiteral(s string, i int, quote byte, swift bool) (string, int) {
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		switch {
		case swift && c == '\\' && i+1 < len(s) && s[i+1] == '(':
			depth, j := 1, i+2
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
				j++
			}
			b.WriteString("{x}")
			i = j
		case !swift && quote == '`' && c == '$' && i+1 < len(s) && s[i+1] == '{':
			depth, j := 1, i+2
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '{':
					depth++
				case '}':
					depth--
				}
				j++
			}
			b.WriteString("{x}")
			i = j
		case c == '\\' && i+1 < len(s):
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i += 2
		case c == quote:
			return b.String(), i + 1
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i
}

func normaliseClientPath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	// "/api/x${qs}" is a path with a query string built by the caller, not a path segment.
	if strings.HasSuffix(p, "{x}") && !strings.HasSuffix(p, "/{x}") {
		p = strings.TrimSuffix(p, "{x}")
	}
	return strings.TrimSuffix(p, "/")
}

var (
	swiftMethod = regexp.MustCompile(`(?:request|send)\(\s*"(GET|POST|PUT|PATCH|DELETE)"\s*,\s*$`)
	webVerb     = regexp.MustCompile(`api\.(get|post|put|patch|del|delete)\b`)
	webMethod   = regexp.MustCompile(`method:\s*["'](GET|POST|PUT|PATCH|DELETE)["']`)
)

func clientCalls(t *testing.T, root string, swift bool) []clientCall {
	t.Helper()
	var calls []clientCall
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if n := info.Name(); n == "node_modules" || n == "dist" || (swift && strings.HasSuffix(n, "Tests")) {
				return filepath.SkipDir
			}
			return nil
		}
		base := info.Name()
		if swift && !strings.HasSuffix(base, ".swift") || !swift && !(strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".tsx")) ||
			strings.Contains(base, ".test.") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		lines := strings.Split(src, "\n")
		for ln, line := range lines {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "//") || strings.HasPrefix(trim, "*") || strings.HasPrefix(trim, "/*") {
				continue
			}
			for off := 0; off < len(line); {
				var q byte
				idx := -1
				for _, cand := range []string{`"/api/`, "`/api/", `'/api/`} {
					if k := strings.Index(line[off:], cand); k >= 0 && (idx < 0 || k < idx) {
						idx, q = k, cand[0]
					}
				}
				if idx < 0 || (swift && q != '"') {
					break
				}
				start := off + idx + 1
				body, end := scanLiteral(line, start, q, swift)
				off = end
				method := "GET"
				before := line[:start-1]
				if swift {
					if m := swiftMethod.FindStringSubmatch(before); m != nil {
						method = m[1]
					} else if ln > 0 {
						if m := swiftMethod.FindStringSubmatch(lines[ln-1] + "\n"); m != nil && strings.TrimSpace(before) == "" {
							method = m[1]
						}
					}
				} else {
					// A call's type arguments can run over several lines, so look back, but stop at the previous call's path.
					ctx := before
					for k := ln - 1; k >= 0 && k >= ln-40; k-- {
						if strings.Contains(lines[k], "/api/") || strings.Contains(lines[k], "fetch(") {
							break
						}
						ctx = lines[k] + " " + ctx
					}
					if m := webVerb.FindAllStringSubmatch(ctx, -1); m != nil {
						v := m[len(m)-1][1]
						method = strings.ToUpper(v)
						if v == "del" {
							method = "DELETE"
						}
					} else {
						tail := strings.Join(lines[ln:min(ln+5, len(lines))], " ")
						if m := webMethod.FindStringSubmatch(tail); m != nil {
							method = m[1]
						}
					}
				}
				calls = append(calls, clientCall{file: path, line: ln + 1, method: method, path: normaliseClientPath(body)})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

type routeSpec struct {
	method string
	segs   []string
}

func splitPath(p string) []string {
	return strings.Split(strings.Trim(p, "/"), "/")
}

func matchRoute(call []string, route []string) bool {
	for i, rs := range route {
		if rs == "*" {
			return len(call) >= i
		}
		if i >= len(call) {
			return false
		}
		if strings.HasPrefix(rs, "{") {
			continue
		}
		if call[i] != rs && call[i] != "{x}" {
			return false
		}
	}
	return len(call) == len(route)
}

// contractRouter is the real router with enough of its dependencies stubbed for the routes the clients call to be mounted.
func contractRouter(t *testing.T) chi.Routes {
	t.Helper()
	t.Setenv("RIVOLT_EVENTS", "true")
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// Passkeys are off in prod today but the web calls their routes, so the check needs them mounted. A configured service on a dead
	// database: nothing is called.
	dead := deadDB(t)
	pk, err := passkeys.New(passkeys.Config{RPID: "example.test", Origins: []string{"https://example.test"}}, passkeys.NewPGStore(dead))
	if err != nil {
		t.Fatal(err)
	}
	// The change-password and reset routes need a sessions store behind the sessions API; a store on the dead database is mounted, never called.
	sessionStore, err := sessions.New(dead, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	// Handlers are mounted, never called, so zero values stand in for every dependency a route is gated on.
	return New(Deps{
		Passkeys: pk,
		Auth:     svc, ValhallaProxy: stub, TilesProxy: stub, AuthEnforced: true,
		DB: dead, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Flags: new(flags.Store), Metrics: nil,
		Email: new(email.Client), StatementSigner: new(statement.Signer), WeeklySigner: new(statement.Signer),
		SignupRequests: new(signuprequests.Store), OIDC: new(oidc.Service), AppAuth: new(AppAuth), Sessions: &SessionsAPI{Store: sessionStore}, SessionRevoker: sessionStore,
		Hydra: new(hydra.Client), Kratos: new(kratos.Client), Changelog: new(changelog.Source), ChargersArchive: new(chargers.Archive),
		Trips: new(trips.Factory), Drives: new(drives.Factory), Charges: new(charges.Factory), Samples: new(samples.Factory),
		Settings: new(settings.Factory), IOSAppIDs: []string{"TEAMID.dev.rivolt.test"},
	}).(chi.Routes)
}

// deadDB is a pool that never connects (nothing listens on port 1): the background jobs New starts fail quietly, the handlers are never called.
func deadDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func routesOf(t *testing.T, r chi.Routes) []routeSpec {
	t.Helper()
	var out []routeSpec
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route != "/api" && !strings.HasPrefix(route, "/api/") {
			return nil // the SPA fallback (/*) would otherwise match every path
		}
		out = append(out, routeSpec{method: method, segs: splitPath(strings.TrimSuffix(route, "/*") + func() string {
			if strings.HasSuffix(route, "/*") {
				return "/*"
			}
			return ""
		}())})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func checkClientCalls(t *testing.T, side, name string, calls []clientCall, routes []routeSpec) {
	t.Helper()
	if len(calls) < 25 {
		t.Fatalf("%s: only %d API calls found in the source: the reader is not seeing the client", name, len(calls))
	}
	used := map[string]bool{}
	var missing []string
	for _, c := range calls {
		key := c.method + " " + c.path
		if _, ok := ignoredClientCalls[side+" "+key]; ok {
			used[side+" "+key] = true
			continue
		}
		cs := splitPath(c.path)
		found := false
		for _, r := range routes {
			if r.method == c.method && matchRoute(cs, r.segs) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, key+"   ("+filepath.ToSlash(strings.TrimPrefix(c.file, "../../"))+":"+fmt.Sprint(c.line)+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("%s calls %s: no such route on the server", name, m)
	}
	for key, why := range ignoredClientCalls {
		if strings.HasPrefix(key, side+" ") && !used[key] {
			t.Errorf("ignore entry %q (%s) matches no call in the %s source: delete it", key, why, name)
		}
	}
}

func TestSwiftClientCallsExistOnTheServer(t *testing.T) {
	checkClientCalls(t, "swift", "the iPhone app", clientCalls(t, "../../ios/Rivolt", true), routesOf(t, contractRouter(t)))
}

func TestWebClientCallsExistOnTheServer(t *testing.T) {
	checkClientCalls(t, "web", "the web", clientCalls(t, "../../web/src", false), routesOf(t, contractRouter(t)))
}

func TestClientCallReaderAndMatcher(t *testing.T) {
	routes := routesOf(t, contractRouter(t))
	if len(routes) < 100 {
		t.Fatalf("only %d routes walked", len(routes))
	}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/vehicles/{x}/lease", true},
		{"GET", "/api/vehicles/{x}/no-such-thing", false},
		{"POST", "/api/vehicles/{x}/lease", false},
		{"GET", "/api/definitely-not-a-route", false},
	} {
		got := false
		for _, r := range routes {
			if r.method == c.method && matchRoute(splitPath(c.path), r.segs) {
				got = true
			}
		}
		if got != c.want {
			t.Errorf("%s %s: matched=%v, want %v", c.method, c.path, got, c.want)
		}
	}
	body, _ := scanLiteral(`/api/a/\(x.map { "\($0)" }.joined())/b?u=\(1)", 2)`, 0, '"', true)
	if body != "/api/a/{x}/b?u={x}" {
		t.Errorf("swift interpolation: %q", body)
	}
	body, _ = scanLiteral("/api/a/${id}/b?${query({a: `x`})}`", 0, '`', false)
	if body != "/api/a/{x}/b?{x}" {
		t.Errorf("web interpolation: %q", body)
	}
}
