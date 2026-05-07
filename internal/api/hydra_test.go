package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
)

// fakeHydra returns an httptest.Server emulating the four Hydra
// admin endpoints we use, plus a Hydra client wired to it.
func fakeHydra(t *testing.T, h http.HandlerFunc) *hydra.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := hydra.New(hydra.Config{AdminURL: srv.URL})
	if err != nil {
		t.Fatalf("hydra.New: %v", err)
	}
	return c
}

// fakeKratos returns a Kratos client whose PublicURL is the given
// httptest handler. AdminURL is unused but required by New.
func fakeKratos(t *testing.T, h http.HandlerFunc) *kratos.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := kratos.New(kratos.Config{
		AdminURL:  "http://unused",
		PublicURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("kratos.New: %v", err)
	}
	return c
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestHydraLoginGET_skip exercises the "Hydra already trusts this
// session" path: GET returns a 302 to redirect_to without prompting.
func TestHydraLoginGET_skip(t *testing.T) {
	hyd := fakeHydra(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/login"):
			_, _ = w.Write([]byte(`{"challenge":"c","subject":"u","skip":true,"client":{"client_name":"X"}}`))
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/login/accept"):
			_, _ = w.Write([]byte(`{"redirect_to":"https://hydra/cb"}`))
		default:
			t.Errorf("unexpected hydra call: %s %s", r.Method, r.URL.Path)
		}
	})
	d := hydraDeps{Hydra: hyd, Kratos: nil, Logger: discardLogger()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=c", nil)
	hydraLoginGET(d).ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d want 302; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "https://hydra/cb" {
		t.Errorf("Location = %q", got)
	}
}

// TestHydraLoginGET_prompt returns JSON metadata when the user
// must be prompted (skip=false).
func TestHydraLoginGET_prompt(t *testing.T) {
	hyd := fakeHydra(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"challenge":"c","skip":false,"client":{"client_id":"argocd","client_name":"ArgoCD"},"requested_scope":["openid","email"]}`))
	})
	d := hydraDeps{Hydra: hyd, Kratos: fakeKratos(t, nil), Logger: discardLogger()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=c", nil)
	hydraLoginGET(d).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got hydraLoginGetResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ClientName != "ArgoCD" || len(got.RequestedScope) != 2 {
		t.Errorf("response: %+v", got)
	}
}

func TestHydraLoginGET_missingChallenge(t *testing.T) {
	d := hydraDeps{Hydra: fakeHydra(t, nil), Kratos: nil, Logger: discardLogger()}
	rr := httptest.NewRecorder()
	hydraLoginGET(d).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rr.Code)
	}
}

// TestHydraLoginPOST_success: Kratos accepts the password, Hydra
// accepts the login, we return {"redirect_to":...} as JSON.
func TestHydraLoginPOST_success(t *testing.T) {
	flowID := "f"
	kr := fakeKratos(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/self-service/login/api" {
			_, _ = w.Write([]byte(`{"id":"` + flowID + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"session":{"identity":{"id":"kratos-id","traits":{"email":"u@x"}}}}`))
	})
	var seenSubject string
	hyd := fakeHydra(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(body, &b)
			seenSubject, _ = b["subject"].(string)
			_, _ = w.Write([]byte(`{"redirect_to":"https://hydra/done"}`))
		}
	})
	d := hydraDeps{Hydra: hyd, Kratos: kr, Logger: discardLogger()}

	body := bytes.NewBufferString(`{"challenge":"c","email":"u@x","password":"pw"}`)
	req := httptest.NewRequest(http.MethodPost, "/login", body).
		WithContext(context.Background())
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hydraLoginPOST(d).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if seenSubject != "kratos-id" {
		t.Errorf("subject sent to hydra = %q", seenSubject)
	}
}

func TestHydraLoginPOST_invalidCreds(t *testing.T) {
	kr := fakeKratos(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"f"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	hyd := fakeHydra(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("hydra must not be called when credentials fail: %s %s", r.Method, r.URL.Path)
	})
	d := hydraDeps{Hydra: hyd, Kratos: kr, Logger: discardLogger()}
	body := bytes.NewBufferString(`{"challenge":"c","email":"u@x","password":"bad"}`)
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	hydraLoginPOST(d).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHydraConsentGET_autoAccepts(t *testing.T) {
	var grantedScopes []string
	hyd := fakeHydra(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"challenge":"c","subject":"u","client":{"client_id":"argo"},"requested_scope":["openid","email","profile"],"requested_access_token_audience":["argo"]}`))
		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(body, &b)
			if scopes, ok := b["grant_scope"].([]any); ok {
				for _, s := range scopes {
					grantedScopes = append(grantedScopes, s.(string))
				}
			}
			_, _ = w.Write([]byte(`{"redirect_to":"https://app/cb"}`))
		}
	})
	d := hydraDeps{Hydra: hyd, Kratos: nil, Logger: discardLogger()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent?consent_challenge=c", nil)
	hydraConsentGET(d).ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(grantedScopes) != 3 {
		t.Errorf("scopes granted: %v", grantedScopes)
	}
}
