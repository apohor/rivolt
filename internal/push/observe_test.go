package push

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Apple's web push service answers 403 BadJwtToken unless the VAPID JWT is ES256, "aud" is exactly the endpoint's origin, "exp"
// is under 24 h ahead and "sub" is a mailto: or https URL. Build the request the way sendOne does and read the claims back.
func TestVAPIDJWTClaimsAppleAccepts(t *testing.T) {
	for _, subject := range []string{"mailto:admin@rivolt.dev", "https://rivolt.dev/contact"} {
		var auth string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusCreated)
		}))
		priv, pub, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			t.Fatal(err)
		}
		// the subscription keys of a browser
		_, browserPub, _ := webpush.GenerateVAPIDKeys()
		authSecret := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
		sub := &webpush.Subscription{Endpoint: ts.URL + "/send/abc", Keys: webpush.Keys{P256dh: browserPub, Auth: authSecret}}
		svc := NewService(nil, VAPID{PublicKey: pub, PrivateKey: priv, Subject: subject}, nil)
		// sendOne refuses a non-https test server, so go through the same option builder it uses.
		opts := svc.webpushOptions()
		opts.HTTPClient = ts.Client() // the shared client refuses loopback
		res, err := webpush.SendNotification([]byte("x"), sub, opts)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		ts.Close()

		jwt := strings.TrimPrefix(strings.SplitN(auth, ",", 2)[0], "vapid t=")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Fatalf("subject %s: authorization is not a JWT", subject)
		}
		var hdr struct{ Alg string }
		var claims struct {
			Aud string
			Exp int64
			Sub string
		}
		for i, dst := range []any{&hdr, &claims} {
			b, _ := base64.RawURLEncoding.DecodeString(parts[i])
			if err := json.Unmarshal(b, dst); err != nil {
				t.Fatal(err)
			}
		}
		if hdr.Alg != "ES256" {
			t.Errorf("alg %q, want ES256", hdr.Alg)
		}
		if claims.Aud != ts.URL {
			t.Errorf("aud %q, want the endpoint origin %q", claims.Aud, ts.URL)
		}
		if d := time.Until(time.Unix(claims.Exp, 0)); d <= 0 || d > 24*time.Hour {
			t.Errorf("exp is %v ahead, want within 24h", d)
		}
		if claims.Sub != subject {
			t.Errorf("sub %q, want %q (a doubled mailto: is BadJwtToken)", claims.Sub, subject)
		}
		if !strings.Contains(auth, "k="+pub) {
			t.Errorf("the k= key is not the configured public key")
		}
	}
}

func TestCheckVAPIDSubject(t *testing.T) {
	for s, ok := range map[string]bool{
		"mailto:admin@rivolt.dev":     true,
		"https://rivolt.dev/contact":  true,
		"mailto:rivolt@invalid":       false,
		"mailto:me@example.com":       false,
		"mailto:me@localhost":         false,
		"admin@rivolt.dev":            false,
		"http://rivolt.dev":           false,
		"mailto:":                     false,
		"https://example.org":         false,
		"mailto:ops@mail.example.com": false,
	} {
		if err := CheckVAPIDSubject(s); (err == nil) != ok {
			t.Errorf("CheckVAPIDSubject(%q) = %v, want ok=%v", s, err, ok)
		}
	}
}

func TestHostClassAndObserve(t *testing.T) {
	for e, want := range map[string]string{
		"https://web.push.apple.com/x":                "apple",
		"https://fcm.googleapis.com/fcm/send/x":       "fcm",
		"https://updates.push.services.mozilla.com/x": "mozilla",
		"https://wns2-par02p.notify.windows.com/x":    "windows",
		"https://elsewhere.test/x":                    "other",
	} {
		if got := HostClass(e); got != want {
			t.Errorf("HostClass(%s) = %s, want %s", e, got, want)
		}
	}
	var got []string
	svc := NewService(nil, VAPID{}, nil).ObserveSends(func(c, h, o string) { got = append(got, c+"/"+h+"/"+o) })
	svc.observeSend("web", "https://web.push.apple.com/x", "rejected")
	svc.observeSend("apns", "", "ok")
	if len(got) != 2 || got[0] != "web/apple/rejected" || got[1] != "apns/apple/ok" {
		t.Errorf("observed %v", got)
	}
	_ = context.Background
}
