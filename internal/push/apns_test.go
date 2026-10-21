package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

const testDeviceToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// apnsServer is an HTTP/2 TLS server standing in for Apple. handler sees every
// request; the APNs under test points at it.
func apnsServer(t *testing.T, key *ecdsa.PrivateKey, pemKey string, handler http.HandlerFunc) *APNs {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	a, err := NewAPNs(APNsConfig{KeyID: "KEY123ABCD", TeamID: "TEAM123456", KeyPEM: pemKey, Topic: "me.apoh.rivolt"})
	if err != nil {
		t.Fatal(err)
	}
	a.hc = srv.Client()
	a.urls = map[string]string{EnvProduction: srv.URL, EnvSandbox: srv.URL + "/sandbox"}
	_ = key
	return a
}

func TestNewAPNsRefusals(t *testing.T) {
	_, good := testKey(t)
	rsaish := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a key")}))
	for name, cfg := range map[string]APNsConfig{
		"incomplete":   {KeyID: "K", TeamID: "T", KeyPEM: good},
		"not pem":      {KeyID: "K", TeamID: "T", KeyPEM: "hello", Topic: "me.apoh.rivolt"},
		"bad key":      {KeyID: "K", TeamID: "T", KeyPEM: rsaish, Topic: "me.apoh.rivolt"},
		"bad topic":    {KeyID: "K", TeamID: "T", KeyPEM: good, Topic: "not a bundle id!"},
		"blank fields": {KeyID: " ", TeamID: "T", KeyPEM: good, Topic: "me.apoh.rivolt"},
	} {
		if a, err := NewAPNs(cfg); err == nil || a != nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), "hello") {
			t.Errorf("%s: the error leaks key text: %v", name, err)
		}
	}
	if !(APNsConfig{KeyID: "K", TeamID: "T", KeyPEM: good, Topic: "me.apoh.rivolt"}).Enabled() || (APNsConfig{}).Enabled() {
		t.Error("Enabled")
	}
}

// Apple gets the generic line and an id, nothing else; the request is HTTP/2 with
// the topic, push type, priority, expiration and a valid ES256 provider token.
func TestAPNsSendRequest(t *testing.T) {
	key, pemKey := testKey(t)
	var got struct {
		sync.Mutex
		n      int
		method string
		path   string
		proto  int
		hdr    http.Header
		body   []byte
	}
	a := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		got.Lock()
		defer got.Unlock()
		got.n++
		got.method, got.path, got.proto, got.hdr = r.Method, r.URL.Path, r.ProtoMajor, r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})
	id := "6f1b3c1e-0b0e-4c1e-8a64-7d9b2f6f2a10"
	if err := a.Send(t.Context(), EnvProduction, testDeviceToken, id); err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/3/device/"+testDeviceToken || got.proto != 2 {
		t.Errorf("request: %s %s over HTTP/%d", got.method, got.path, got.proto)
	}
	if got.hdr.Get("apns-topic") != "me.apoh.rivolt" || got.hdr.Get("apns-push-type") != "alert" || got.hdr.Get("apns-priority") != "10" {
		t.Errorf("headers: %v", got.hdr)
	}
	exp, _ := strconv.ParseInt(got.hdr.Get("apns-expiration"), 10, 64)
	if d := time.Until(time.Unix(exp, 0)); d < 55*time.Minute || d > 65*time.Minute {
		t.Errorf("apns-expiration is %v away, want about an hour", d)
	}
	// Exactly the generic payload: no kind, no text, no url.
	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	aps, _ := body["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	if len(body) != 2 || body["id"] != id || len(aps) != 2 || aps["mutable-content"] != float64(1) || alert["title"] != "Rivolt" || alert["body"] != "Rivolt update" || len(alert) != 2 {
		t.Errorf("payload = %s", got.body)
	}
	// The provider token: ES256, kid, iss and iat, verifiable with the public key.
	auth := got.hdr.Get("authorization")
	if !strings.HasPrefix(auth, "bearer ") {
		t.Fatalf("authorization = %q", auth)
	}
	jws, err := jose.ParseSigned(strings.TrimPrefix(auth, "bearer "), []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	if jws.Signatures[0].Header.KeyID != "KEY123ABCD" {
		t.Errorf("kid = %q", jws.Signatures[0].Header.KeyID)
	}
	payload, err := jws.Verify(&key.PublicKey)
	if err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
	}
	_ = json.Unmarshal(payload, &claims)
	if claims.Iss != "TEAM123456" || time.Since(time.Unix(claims.Iat, 0)) > time.Minute {
		t.Errorf("claims = %+v", claims)
	}

	// Cached: a second send reuses the token; after 50 minutes a new one is signed.
	first := auth
	_ = a.Send(t.Context(), EnvProduction, testDeviceToken, id)
	if got.hdr.Get("authorization") != first {
		t.Error("the provider token was re-signed within its lifetime")
	}
	a.now = func() time.Time { return time.Now().Add(51 * time.Minute) }
	_ = a.Send(t.Context(), EnvProduction, testDeviceToken, id)
	if got.hdr.Get("authorization") == first {
		t.Error("the provider token was reused past 50 minutes")
	}
	// The sandbox is its own endpoint.
	_ = a.Send(t.Context(), EnvSandbox, testDeviceToken, id)
	if !strings.HasPrefix(got.path, "/sandbox/3/device/") {
		t.Errorf("sandbox path = %q", got.path)
	}
}

func TestAPNsErrors(t *testing.T) {
	key, pemKey := testKey(t)
	status, reason := 200, ""
	a := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		if status == 200 {
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
	})
	for _, c := range []struct {
		status int
		reason string
		dead   bool
	}{
		{410, "Unregistered", true},
		{400, "BadDeviceToken", true},
		{400, "DeviceTokenNotForTopic", true},
		{410, "", true},
		{400, "PayloadEmpty", false},
		{429, "TooManyRequests", false},
		{503, "ServiceUnavailable", false},
		{403, "InvalidProviderToken", false},
	} {
		status, reason = c.status, c.reason
		err := a.Send(t.Context(), EnvProduction, testDeviceToken, "id")
		ae, ok := err.(*APNsError)
		if !ok || ae.Status != c.status || ae.Reason != c.reason || ae.Dead() != c.dead {
			t.Errorf("%d %s: %v (dead %v)", c.status, c.reason, err, c.dead)
		}
		if err != nil && strings.Contains(err.Error(), testDeviceToken) {
			t.Errorf("the error carries the device token: %v", err)
		}
	}
	// Bad inputs never reach Apple.
	if err := a.Send(t.Context(), "elsewhere", testDeviceToken, "id"); err == nil {
		t.Error("unknown environment accepted")
	}
	if ae, ok := a.Send(t.Context(), EnvProduction, "../etc", "id").(*APNsError); !ok || !ae.Dead() {
		t.Error("a malformed token is not treated as a bad token")
	}
	// A connection failure's error does not carry the URL (which holds the token).
	a.urls[EnvProduction] = "https://127.0.0.1:1"
	if err := a.Send(t.Context(), EnvProduction, testDeviceToken, "id"); err == nil || strings.Contains(err.Error(), testDeviceToken) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("transport error leaks: %v", err)
	}
}

// ExpiredProviderToken refreshes the token and retries once.
func TestAPNsExpiredProviderTokenRetriesOnce(t *testing.T) {
	key, pemKey := testKey(t)
	var mu sync.Mutex
	var tokens []string
	a := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokens = append(tokens, r.Header.Get("authorization"))
		n := len(tokens)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"reason":"ExpiredProviderToken"}`))
		}
	})
	a.now = func() time.Time { return time.Now() }
	// Move the clock between the two signings so the tokens differ.
	calls := 0
	a.now = func() time.Time { calls++; return time.Now().Add(time.Duration(calls) * time.Second) }
	if err := a.Send(t.Context(), EnvProduction, testDeviceToken, "id"); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0] == tokens[1] {
		t.Errorf("tokens seen: %d, same=%v", len(tokens), len(tokens) == 2 && tokens[0] == tokens[1])
	}
	// A second expiry is not retried again.
	tokens = nil
	a2 := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokens = append(tokens, "x")
		mu.Unlock()
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"reason":"ExpiredProviderToken"}`))
	})
	if err := a2.Send(t.Context(), EnvProduction, testDeviceToken, "id"); err == nil || len(tokens) != 2 {
		t.Errorf("expired twice: err=%v, %d requests, want 2 and an error", err, len(tokens))
	}
}

func TestSafeNotificationURLAndSelector(t *testing.T) {
	for in, want := range map[string]string{
		"/charges/abc": "/charges/abc", "/": "/", "": "/", "https://evil.example/x": "/", "//evil.example": "/",
		"/a\\b": "/", "/a b": "/", "/a\nb": "/", "javascript:alert(1)": "/", strings.Repeat("/a", 101): "/",
	} {
		if got := SafeNotificationURL(in); got != want {
			t.Errorf("SafeNotificationURL(%q) = %q, want %q", in, got, want)
		}
	}
	p := Preferences{OnChargingDone: true, OnAnomaly: true, OnSoftwareUpdate: true}
	for kind, want := range map[string]bool{"charging_done": true, "anomaly": true, "software_update": true, "plug_in_reminder": false, "milestone": false, "test": false, "": false} {
		if got := apnsSelector(kind, p); got != want {
			t.Errorf("selector %q = %v, want %v", kind, got, want)
		}
	}
}

// A token that is not hex (a bad value in the database) never reaches the
// request path: Send refuses it before any request, and says it is dead.
func TestAPNsSendRefusesMalformedTokenBeforeRequest(t *testing.T) {
	key, pemKey := testKey(t)
	var mu sync.Mutex
	hits := 0
	a := apnsServer(t, key, pemKey, func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	})
	for _, tok := range []string{"", "abc", strings.Repeat("a", 63), strings.Repeat("a", 201),
		strings.Repeat("a", 64) + "/../x", strings.Repeat("g", 64), strings.Repeat("a", 64) + "?x=1"} {
		err := a.Send(t.Context(), EnvProduction, tok, "id-1")
		ae, ok := err.(*APNsError)
		if !ok || !ae.Dead() {
			t.Errorf("token %q: err = %v, want a dead-token error", tok, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("%d requests reached Apple for malformed tokens", hits)
	}
}

// The Live Activity payload is exactly aps{timestamp, event, content-state{soc_pct,
// power_kw, minutes_to_target}} and nothing else, for an update and for the end:
// no strings, no names, no places (DA's condition).
func TestLiveBodyHasExactlyTheAllowedKeys(t *testing.T) {
	for _, event := range []string{LiveUpdate, LiveEnd} {
		raw := liveBody(event, time.Unix(1790000000, 0), NewLiveState(64.4, 11.46, 42.2))
		var top map[string]json.RawMessage
		if err := json.Unmarshal(raw, &top); err != nil {
			t.Fatal(err)
		}
		if len(top) != 1 || top["aps"] == nil {
			t.Fatalf("%s: top-level keys = %s", event, raw)
		}
		var aps map[string]json.RawMessage
		_ = json.Unmarshal(top["aps"], &aps)
		keys := func(m map[string]json.RawMessage) string {
			var ks []string
			for k := range m {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			return strings.Join(ks, ",")
		}
		if keys(aps) != "content-state,event,timestamp" {
			t.Errorf("%s: aps keys = %s", event, keys(aps))
		}
		var cs map[string]json.RawMessage
		_ = json.Unmarshal(aps["content-state"], &cs)
		if keys(cs) != "minutes_to_target,power_kw,soc_pct" {
			t.Errorf("%s: content-state keys = %s", event, keys(cs))
		}
		var event2 string
		_ = json.Unmarshal(aps["event"], &event2)
		if event2 != event || string(aps["timestamp"]) != "1790000000" {
			t.Errorf("%s: event/timestamp = %s %s", event, aps["event"], aps["timestamp"])
		}
		// Numbers only inside the content state: no quotes at all.
		if strings.Count(string(aps["content-state"]), `"`) != 6 { // three keys, three numbers
			t.Errorf("%s: content-state holds a string: %s", event, aps["content-state"])
		}
		if string(cs["soc_pct"]) != "64" || string(cs["power_kw"]) != "11.5" || string(cs["minutes_to_target"]) != "42" {
			t.Errorf("%s: figures = %s", event, aps["content-state"])
		}
	}
}

func TestNewLiveStateClampsAndRounds(t *testing.T) {
	for _, c := range []struct {
		soc, kw, min float64
		want         LiveState
	}{
		{64.5, 11.46, 42.4, LiveState{65, 11.5, 42}},
		{-3, -1, -5, LiveState{0, 0, 0}},
		{130, 250.04, 9999.6, LiveState{100, 250, 10000}},
	} {
		if got := NewLiveState(c.soc, c.kw, c.min); got != c.want {
			t.Errorf("NewLiveState(%v,%v,%v) = %+v, want %+v", c.soc, c.kw, c.min, got, c.want)
		}
	}
}

// A Live Activity update goes to the activity token's path with the live-activity
// push type, the app's topic plus the live suffix, low priority for an update and
// high for the end; a malformed token never reaches the request.
func TestAPNsSendLive(t *testing.T) {
	key, pemKey := testKey(t)
	type seen struct{ path, pushType, topic, prio, body string }
	var mu sync.Mutex
	var got []seen
	a := apnsServer(t, key, pemKey, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.URL.Path, r.Header.Get("apns-push-type"), r.Header.Get("apns-topic"), r.Header.Get("apns-priority"), string(b)})
		mu.Unlock()
	})
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef"
	st := NewLiveState(50, 10, 30)
	if err := a.SendLive(t.Context(), EnvProduction, tok, LiveUpdate, st); err != nil {
		t.Fatal(err)
	}
	if err := a.SendLive(t.Context(), EnvProduction, tok, LiveEnd, st); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "zz", tok + "/../x", tok + "?x=1"} {
		if err := a.SendLive(t.Context(), EnvProduction, bad, LiveUpdate, st); err == nil {
			t.Errorf("token %q was sent", bad)
		}
	}
	if err := a.SendLive(t.Context(), EnvProduction, tok, "alert", st); err == nil {
		t.Error("an unknown event was sent")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("%d requests reached Apple, want 2: %+v", len(got), got)
	}
	for i, want := range []struct{ event, prio string }{{LiveUpdate, "5"}, {LiveEnd, "10"}} {
		g := got[i]
		if g.path != "/3/device/"+tok || g.pushType != "liveactivity" || g.topic != "me.apoh.rivolt.push-type.liveactivity" || g.prio != want.prio {
			t.Errorf("%s: %+v", want.event, g)
		}
		if !strings.Contains(g.body, `"event":"`+want.event+`"`) {
			t.Errorf("%s: body = %s", want.event, g.body)
		}
	}
}
