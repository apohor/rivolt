package push

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestValidEndpoint(t *testing.T) {
	for _, ok := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/xyz",
		"https://web.push.apple.com/QAbc",
		"https://api.push.apple.com/3/device/x",
		"https://wns2-par02p.notify.windows.com/w/?token=x",
		"https://FCM.googleapis.com:443/fcm/send/abc",
	} {
		if err := ValidEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"http":             "http://fcm.googleapis.com/fcm/send/abc",
		"internal host":    "https://kubernetes.default.svc/api",
		"metadata IP":      "https://169.254.169.254/latest/meta-data",
		"loopback":         "https://127.0.0.1:8080/x",
		"IPv6 literal":     "https://[::1]/x",
		"decimal IP":       "https://2130706433/x",
		"unlisted host":    "https://evil.example/push",
		"suffix trick":     "https://fcm.googleapis.com.evil.example/x",
		"prefix trick":     "https://evilfcm.googleapis.com.attacker.net/x",
		"credentials":      "https://user:pw@fcm.googleapis.com/x",
		"odd port":         "https://fcm.googleapis.com:8443/x",
		"bare suffix":      "https://notify.windows.com/x",
		"empty":            "",
		"not a url":        "::::",
		"other googleapis": "https://storage.googleapis.com/x",
		"javascript":       "javascript:alert(1)",
	} {
		if err := ValidEndpoint(bad); !errors.Is(err, ErrEndpoint) {
			t.Errorf("%s (%s): %v, want ErrEndpoint", name, bad, err)
		}
	}
}

func TestPrivateAddressesAreRefused(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "::1", "fe80::1", "fc00::1", "::ffff:10.0.0.1", "224.0.0.1", "198.18.0.1"} {
		if !privateAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s not refused", a)
		}
	}
	for _, a := range []string{"8.8.8.8", "142.250.80.46", "2607:f8b0:4004:c1b::5f"} {
		if privateAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s refused", a)
		}
	}
}

// The push client connects to nothing private, follows no redirect and has a timeout.
func TestPushHTTPClient(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer srv.Close()
	c := newPushHTTPClient()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil) // 127.0.0.1
	if _, err := c.Do(req); err == nil || hits != 0 {
		t.Fatalf("the loopback server was reached: err=%v hits=%d", err, hits)
	}
	if c.Timeout < time.Second || c.Timeout > time.Minute {
		t.Errorf("timeout %v", c.Timeout)
	}
	if err := c.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("redirects are followed: %v", err)
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Error("an environment proxy could be used")
	}
}

// The error a failed send returns reaches the API (the test route): it carries the
// status and never the push service's body.
func TestSendErrorHasNoBody(t *testing.T) {
	e := &sendError{status: 403, body: "internal host says: secret"}
	if got := e.Error(); got != "push status 403" {
		t.Errorf("%q", got)
	}
}
