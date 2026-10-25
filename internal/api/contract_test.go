package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// API contract tests (Sprint 11). The fixtures in ios/RivoltTests/Fixtures are server responses shared by Go, the iPhone app and
// (next) the web. TestIOSFixturesMatchServerTypes decodes each strictly into the server's own type; the tests here close the
// gaps around that: no fixture can skip it, and a few keys must never appear.

const iosFixtureDir = "../../ios/RivoltTests/Fixtures"

// fixturesCheckedElsewhere are fixtures that are not decoded into a single server type by iosFixtureTargets, each with where they
// are checked instead. An entry for a file that no longer exists, or without a reason, fails the test, so the list cannot rot.
var fixturesCheckedElsewhere = map[string]string{
	"charge_eta_vectors.json":        "test vectors for the charge-time estimate, checked in internal/rivian/charge_eta_test.go",
	"ledger_savings_flat.json":       "the flat-rate savings answer, checked in flat_rate_test.go",
	"sleep_health.json":              "the sleep-health card, checked in sleep_health_fixture_test.go",
	"err_plan_quota.json":            "an error answer ({status, retry_after_header, body}) generated from writeQuotaError, checked in trip_fixtures_test.go",
	"err_upstream_user_action.json":  "an error answer generated from writeUpstreamError, checked in trip_fixtures_test.go",
	"err_upstream_transient.json":    "an error answer generated from writeUpstreamError, checked in trip_fixtures_test.go",
	"err_upstream_rate_limited.json": "an error answer generated from writeUpstreamError, checked in trip_fixtures_test.go",
	"err_no_position.json":           "an error answer generated from noPosition, checked in trip_fixtures_test.go",
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(iosFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// A fixture the server never decodes proves nothing: every file in the directory is mapped to a server type or listed with the
// test that covers it, and every mapping and every list entry names a file that exists.
func TestEveryIOSFixtureIsChecked(t *testing.T) {
	targets := iosFixtureTargets()
	have := map[string]bool{}
	for _, n := range fixtureNames(t) {
		have[n] = true
		_, mapped := targets[n]
		_, elsewhere := fixturesCheckedElsewhere[n]
		if !mapped && !elsewhere {
			t.Errorf("%s is in %s but no server type decodes it: add it to iosFixtureTargets (or to fixturesCheckedElsewhere with the test that covers it)", n, iosFixtureDir)
		}
		if mapped && elsewhere {
			t.Errorf("%s is both mapped and listed as checked elsewhere", n)
		}
	}
	for n := range targets {
		if !have[n] {
			t.Errorf("iosFixtureTargets names %s, which is not in %s", n, iosFixtureDir)
		}
	}
	for n, why := range fixturesCheckedElsewhere {
		if !have[n] {
			t.Errorf("fixturesCheckedElsewhere names %s, which is not in %s", n, iosFixtureDir)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("fixturesCheckedElsewhere[%s] has no reason", n)
		}
	}
}

// Never-keys. For a few responses, ADDING a field is the regression: it would put something where the code promises it is not.
// The lists are deliberate; changing one needs the devils-advocate's ack.
var (
	// The activity feed carries no places: "no coordinates, no place name". Its `place` key is the home/away CLASS ("home",
	// "away" or ""), not a name, so it is allowed and its values are pinned instead (activityPlaceClasses). A change that lets `place`
	// carry a name (or widens the pinned set) needs the devils-advocate's approval.
	activityNeverKeys    = []string{"lat", "lon", "latitude", "longitude", "address", "place_name", "city", "street"}
	activityPlaceClasses = map[string]bool{"home": true, "away": true, "": true}
	// What the anonymous demo serves, and /api/app/config, carries nothing about a person or a session.
	demoNeverKeys = []string{"email", "ip", "user_agent", "token"}
)

// neverEverKey: no response carries a secret or a token under a name that says so (the app's sign-in grant is `token`, which
// is not this).
func neverEverKey(k string) bool {
	k = strings.ToLower(k)
	return k == "secret" || strings.HasSuffix(k, "_token") || strings.HasSuffix(k, "_secret")
}

// demoServedFixtures: the fixtures of the endpoints the anonymous demo serves (and /api/app/config), by route. Each route must
// be on the demo's allowlist, so the table cannot rot.
var demoServedFixtures = map[string][]string{
	"/api/vehicles":               {"vehicles.json"},
	"/api/charges":                {"charges_last.json"},
	"/api/drives":                 {"drives_last.json"},
	"/api/activity":               {"activity.json", "activity_live_shape.json"},
	"/api/chart-points":           {"chart_points.json"},
	"/api/charge-split":           {"charge_split.json"},
	"/api/ledger/summary":         {"ledger_summary.json"},
	"/api/ledger/monthly":         {"ledger_monthly.json"},
	"/api/ledger/savings":         {"ledger_savings.json", "ledger_savings_needs.json"},
	"/api/milestones":             {"milestones.json"},
	"/api/sleep-activity":         {"sleep_activity.json"},
	"/api/charges/clusters":       {"charge_clusters.json"},
	"/api/insights/efficiency":    {"efficiency.json"},
	"/api/settings/charging":      {"settings_charging.json"},
	"/api/settings/units":         {"settings_units.json"},
	"/api/settings/home-location": {"settings_home_location.json"},
	"/api/app/config":             {"app_config.json"},
}

// jsonKeys returns every object key at any depth of a decoded JSON value.
func jsonKeys(v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			out[k] = true
			jsonKeys(e, out)
		}
	case []any:
		for _, e := range x {
			jsonKeys(e, out)
		}
	}
}

// typeKeys returns every JSON key a Go type can produce, at any depth, from its struct tags (embedded structs inlined; a field
// tagged "-" skipped; time.Time and the like are leaves).
func typeKeys(t reflect.Type, out map[string]bool, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Time{}) || seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			typeKeys(f.Type, out, seen)
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = true
		typeKeys(f.Type, out, seen)
	}
}

// collectValues gathers the string values of every key called key, at any depth.
func collectValues(v any, key string, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k == key {
				if str, ok := e.(string); ok {
					*out = append(*out, str)
				}
			}
			collectValues(e, key, out)
		}
	case []any:
		for _, e := range x {
			collectValues(e, key, out)
		}
	}
}

func readFixture(t *testing.T, name string) any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(iosFixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

func forbiddenKeys(keys map[string]bool, deny []string) []string {
	var bad []string
	for _, d := range deny {
		for k := range keys {
			if strings.EqualFold(k, d) {
				bad = append(bad, k)
			}
		}
	}
	sort.Strings(bad)
	return bad
}

// The never-keys, on both sides: the keys of every fixture, and the keys the server's type can produce (so a field added to the
// type fails even before anyone writes a fixture for it).
func TestResponsesNeverCarryTheseKeys(t *testing.T) {
	targets := iosFixtureTargets()
	check := func(what string, keys map[string]bool, deny []string, why string) {
		if bad := forbiddenKeys(keys, deny); len(bad) > 0 {
			t.Errorf("%s carries %v: %s", what, bad, why)
		}
	}
	for name, target := range targets {
		fx := map[string]bool{}
		jsonKeys(readFixture(t, name), fx)
		ty := map[string]bool{}
		typeKeys(reflect.TypeOf(target), ty, map[reflect.Type]bool{})
		for _, side := range []struct {
			what string
			keys map[string]bool
		}{{"fixture " + name, fx}, {"the type behind " + name, ty}} {
			for k := range side.keys {
				if neverEverKey(k) {
					t.Errorf("%s has a %q key: no response carries a secret or a *_token", side.what, k)
				}
			}
		}
		if name == "activity.json" || name == "activity_live_shape.json" {
			var places []string
			collectValues(readFixture(t, name), "place", &places)
			for _, p := range places {
				if !activityPlaceClasses[p] {
					t.Errorf("fixture activity.json has place=%q: the activity feed's place is the class home or away, never a name", p)
				}
			}
			check("fixture activity.json", fx, activityNeverKeys, "the activity feed has no coordinates and no place names")
			check("the type behind activity.json", ty, activityNeverKeys, "the activity feed has no coordinates and no place names")
		}
	}
	for route, names := range demoServedFixtures {
		for _, name := range names {
			target, ok := targets[name]
			if !ok {
				t.Errorf("demoServedFixtures[%s] names %s, which is not a mapped fixture", route, name)
				continue
			}
			fx := map[string]bool{}
			jsonKeys(readFixture(t, name), fx)
			ty := map[string]bool{}
			typeKeys(reflect.TypeOf(target), ty, map[reflect.Type]bool{})
			check("fixture "+name+" ("+route+")", fx, demoNeverKeys, "the anonymous demo serves nothing about a person or a session")
			check("the type behind "+name+" ("+route+")", ty, demoNeverKeys, "the anonymous demo serves nothing about a person or a session")
		}
	}
}

// demoServedFixtures cannot name a route the demo does not serve.
func TestDemoServedFixturesAreDemoRoutes(t *testing.T) {
	for route := range demoServedFixtures {
		if route == "/api/app/config" {
			continue // public on every host, not part of the demo allowlist
		}
		if !demoExactPaths[route] {
			t.Errorf("%s is in demoServedFixtures but not in the demo allowlist: remove it, or the demo no longer serves it", route)
		}
	}
}

// The walker itself, on a made-up type: a nested, tagged, embedded and omitted field are all seen or skipped as stated.
func TestTypeKeysWalker(t *testing.T) {
	type inner struct {
		Lat float64 `json:"lat"`
	}
	type base struct {
		Email string `json:"email,omitempty"`
	}
	type sample struct {
		base
		Rows   []inner           `json:"rows"`
		Note   map[string]*inner `json:"note"`
		Skip   string            `json:"-"`
		At     time.Time         `json:"at"`
		hidden string
	}
	_ = sample{hidden: "read, so the compiler and the linter see it used"}.hidden
	keys := map[string]bool{}
	typeKeys(reflect.TypeOf(&sample{}), keys, map[reflect.Type]bool{})
	for _, want := range []string{"rows", "note", "at", "lat", "email"} {
		if !keys[want] {
			t.Errorf("walker missed %q: %v", want, keys)
		}
	}
	if keys["-"] || keys["Skip"] || keys["hidden"] {
		t.Errorf("walker saw a field it should skip: %v", keys)
	}
	if bad := forbiddenKeys(map[string]bool{"Latitude": true, "x": true}, activityNeverKeys); len(bad) != 1 {
		t.Errorf("case-insensitive deny = %v", bad)
	}
	if !neverEverKey("access_token") || !neverEverKey("Secret") || !neverEverKey("client_secret") || neverEverKey("token") || neverEverKey("tokens_used") {
		t.Error("neverEverKey is wrong")
	}
}
