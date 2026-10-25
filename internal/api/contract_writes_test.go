package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

// Contract check D (Sprint 11): what the iPhone app WRITES agrees with what the server lets an app token write. The existing
// allowlist tests walk the router and the list; this one reads the app's own source (the reader from check B), so it catches the
// two drifts they cannot:
//   - the app starts sending a write the allowlist does not have: every user would get a 403 (this fails the Go build, not QA);
//   - an allowlist entry no app code calls any more (or never did): a write a leaked phone token can make, kept for nothing.

// appWritesNotCheckedByAllowlist lists calls the app makes that are not subject to the allowlist, with why.
var appWritesNotCheckedByAllowlist = map[string]string{
	"POST /api/auth/app/token": "the code-for-token exchange: made before the app has a session, so there is no app token to check",
}

// appAllowedButNotYetSent lists allowlist entries (and patterns) that no app source calls today, each with why it stays. An
// entry that the app does call fails the test, so this list shrinks as the app catches up. An allowed write nothing sends is attack
// surface with no benefit, so each reason carries a deadline: the end of Sprint 11.
var appAllowedButNotYetSent = map[string]string{}

// samplePaths gives the spellings a {x} in an app path can take, so the pattern rules (a UUID, a vehicle id) are exercised.
func samplePaths(p string) []string {
	if !strings.Contains(p, "{x}") {
		return []string{p}
	}
	return []string{
		strings.ReplaceAll(p, "{x}", "3f2b8c1e-5d4a-4e6f-9a7b-0c1d2e3f4a5b"),
		strings.ReplaceAll(p, "{x}", "veh-fixture"),
	}
}

func TestAppWritesAreAllowedByTheServer(t *testing.T) {
	calls := clientCalls(t, "../../ios/Rivolt", true)
	var sent []string // method + path, with {x}
	seen := map[string]bool{}
	for _, c := range calls {
		if c.method == http.MethodGet {
			continue
		}
		key := c.method + " " + c.path
		seen[key] = true
		sent = append(sent, key)
	}
	if len(sent) < 10 {
		t.Fatalf("only %d app writes found in the source: the reader is not seeing the app", len(sent))
	}

	// Forward: everything the app sends is allowed (or is a listed pre-session call).
	for key := range seen {
		if _, ok := appWritesNotCheckedByAllowlist[key]; ok {
			continue
		}
		method, p, _ := strings.Cut(key, " ")
		allowed := false
		for _, sp := range samplePaths(p) {
			allowed = allowed || appWriteAllowed(method, sp)
		}
		if !allowed {
			t.Errorf("the app sends %s but the server refuses it for an app token (add it to appWrites in its own PR, or fix the app)", key)
		}
	}
	for key, why := range appWritesNotCheckedByAllowlist {
		if !seen[key] {
			t.Errorf("appWritesNotCheckedByAllowlist %q (%s) matches no call in the app: delete it", key, why)
		}
	}

	// Reverse: every allowlist entry is sent by the app, or is listed as not yet sent.
	var entries []string
	for k := range appWrites {
		entries = append(entries, k)
	}
	entries = append(entries, "DELETE /api/auth/sessions/{x}", "PUT /api/vehicles/{id}/profile")
	sort.Strings(entries)
	for _, e := range entries {
		called := seen[e] || (strings.HasSuffix(e, "/{id}/profile") && seen["PUT /api/vehicles/{x}/profile"])
		_, listed := appAllowedButNotYetSent[e]
		switch {
		case !called && !listed:
			t.Errorf("%s is allowed for an app token but no app code sends it: remove it from the allowlist, or list it in appAllowedButNotYetSent with why", e)
		case called && listed:
			t.Errorf("%s is in appAllowedButNotYetSent but the app now sends it: delete the entry", e)
		}
	}
	for e, why := range appAllowedButNotYetSent {
		if !appWriteAllowed(strings.SplitN(e, " ", 2)[0], strings.NewReplacer("{id}", "veh-fixture").Replace(strings.SplitN(e, " ", 2)[1])) {
			t.Errorf("appAllowedButNotYetSent %q (%s) is not on the allowlist at all: delete it", e, why)
		}
	}
}
