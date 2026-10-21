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
