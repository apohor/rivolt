package kratos

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// startTestServer returns an httptest.Server whose handler is the
// caller-supplied func, plus a *Client wired to talk to it.
func startTestServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Config{AdminURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, c
}

func TestNewFromEnv_disabled(t *testing.T) {
	t.Setenv("KRATOS_ADMIN_URL", "")
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if c.Enabled() {
		t.Fatalf("expected disabled when KRATOS_ADMIN_URL is empty")
	}
}

func TestCreateIdentity_success(t *testing.T) {
	var got identityRequest
	_, c := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/identities" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"00000000-0000-0000-0000-000000000001"}`))
	})

	if err := c.CreateIdentity(context.Background(), "u@example.com", "User One", "admin", "hunter2"); err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if got.SchemaID != "rivolt_user" {
		t.Errorf("schema_id = %q, want rivolt_user", got.SchemaID)
	}
	if got.Traits.Email != "u@example.com" {
		t.Errorf("email = %q", got.Traits.Email)
	}
	if got.Traits.DisplayName != "User One" {
		t.Errorf("display_name = %q", got.Traits.DisplayName)
	}
	if got.MetadataPublic.Role != "admin" {
		t.Errorf("role = %q", got.MetadataPublic.Role)
	}
	if got.Credentials == nil || got.Credentials.Password == nil ||
		got.Credentials.Password.Config.Password != "hunter2" {
		t.Errorf("password not propagated: %+v", got.Credentials)
	}
}

func TestCreateIdentity_conflict(t *testing.T) {
	_, c := startTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":409,"message":"conflict"}}`))
	})
	err := c.CreateIdentity(context.Background(), "dup@example.com", "", "user", "pw")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}

func TestCreateIdentityGeneratePassword(t *testing.T) {
	var got identityRequest
	_, c := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusCreated)
	})
	pw, err := c.CreateIdentityGeneratePassword(context.Background(), "x@example.com", "X", "user")
	if err != nil {
		t.Fatalf("CreateIdentityGeneratePassword: %v", err)
	}
	if len(pw) < 16 {
		t.Errorf("generated password too short: %q", pw)
	}
	if got.Credentials == nil || got.Credentials.Password.Config.Password != pw {
		t.Errorf("server-side password (%q) does not match returned (%q)",
			got.Credentials.Password.Config.Password, pw)
	}
}

func TestNormalizeRole(t *testing.T) {
	cases := map[string]string{
		"admin":   "admin",
		"Admins":  "admin",
		"  USER ": "user",
		"":        "user",
		"weird":   "user",
	}
	for in, want := range cases {
		if got := normalizeRole(in); got != want {
			t.Errorf("normalizeRole(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeleteIdentity_lookupAndDelete(t *testing.T) {
	deleted := false
	_, c := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/identities":
			if got := r.URL.Query().Get("credentials_identifier"); got != "u@example.com" {
				t.Errorf("credentials_identifier = %q", got)
			}
			_, _ = w.Write([]byte(`[{"id":"abc","traits":{"email":"u@example.com"}}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/admin/identities/abc":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	if err := c.DeleteIdentity(context.Background(), "u@example.com"); err != nil {
		t.Fatalf("DeleteIdentity: %v", err)
	}
	if !deleted {
		t.Errorf("DELETE was not invoked")
	}
}

func TestDeleteIdentity_missingIsNoop(t *testing.T) {
	_, c := startTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	})
	if err := c.DeleteIdentity(context.Background(), "missing@example.com"); err != nil {
		t.Errorf("expected no-op for missing user, got %v", err)
	}
}

func TestNilClientDisabled(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Fatalf("nil client must be disabled")
	}
	if err := c.CreateIdentity(context.Background(), "x", "", "user", "pw"); err == nil {
		t.Errorf("nil CreateIdentity should error")
	}
	if _, err := c.CreateIdentityGeneratePassword(context.Background(), "x", "", "user"); err == nil {
		t.Errorf("nil CreateIdentityGeneratePassword should error")
	}
	if err := c.DeleteIdentity(context.Background(), "x"); err == nil {
		t.Errorf("nil DeleteIdentity should error")
	}
}

func TestIdentity_EmailVerified(t *testing.T) {
	id := Identity{Traits: IdentityTraits{Email: " Boss@Example.com "}}
	if id.EmailVerified() {
		t.Fatal("no verifiable addresses must be unverified")
	}
	id.VerifiableAddresses = []VerifiableAddress{{Value: "boss@example.com", Verified: false}}
	if id.EmailVerified() {
		t.Fatal("unverified address must not count")
	}
	id.VerifiableAddresses = []VerifiableAddress{
		{Value: "other@example.com", Verified: true},
		{Value: "BOSS@example.com", Verified: true},
	}
	if !id.EmailVerified() {
		t.Fatal("verified trait email (case-insensitive) must count")
	}
	id.VerifiableAddresses = []VerifiableAddress{{Value: "other@example.com", Verified: true}}
	if id.EmailVerified() {
		t.Fatal("a different verified address must not verify the trait email")
	}
}

func TestRevokeIdentitySessions(t *testing.T) {
	var got string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RevokeIdentitySessions(context.Background(), "id-1"); err != nil || got != "DELETE /admin/identities/id-1/sessions" {
		t.Fatalf("got %q, %v", got, err)
	}
	status = http.StatusNotFound
	if err := c.RevokeIdentitySessions(context.Background(), "id-1"); err != nil {
		t.Errorf("a gone identity is success: %v", err)
	}
	status = http.StatusInternalServerError
	if err := c.RevokeIdentitySessions(context.Background(), "id-1"); err == nil {
		t.Error("a 500 must be an error")
	}
	if err := c.RevokeIdentitySessions(context.Background(), ""); err == nil {
		t.Error("an empty id must be refused")
	}
}

// SetPassword is a full replace (Kratos' PATCH cannot touch credentials), so everything else on the identity must come back
// unchanged: the test sends back exactly what the server held, except for the password.
func TestSetPasswordKeepsEveryOtherField(t *testing.T) {
	const held = `{
		"id": "kid-1", "schema_id": "default", "state": "active", "state_changed_at": "2026-10-01T00:00:00Z",
		"traits": {"email": "owner@x.test", "display_name": "Owner", "future_trait": {"a": [1, 2, 3]}},
		"metadata_public": {"role": "admin", "extra": true},
		"metadata_admin": {"note": "kept", "n": 7},
		"external_id": "ext-9",
		"verifiable_addresses": [{"value": "owner@x.test", "verified": true}],
		"recovery_addresses": [{"value": "owner@x.test"}],
		"credentials": {"password": {"type": "password"}}
	}`
	var put map[string]json.RawMessage
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(held))
		case http.MethodPut:
			method, path = r.Method, r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&put)
			_, _ = w.Write([]byte(held))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetPassword(context.Background(), "kid-1", "a-new-password-1"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/admin/identities/kid-1" {
		t.Fatalf("%s %s", method, path)
	}
	var heldMap map[string]json.RawMessage
	_ = json.Unmarshal([]byte(held), &heldMap)
	for _, k := range []string{"schema_id", "state", "traits", "metadata_public", "metadata_admin", "external_id"} {
		var a, b any
		_ = json.Unmarshal(heldMap[k], &a)
		if err := json.Unmarshal(put[k], &b); err != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("%s changed: held %s, sent %s", k, heldMap[k], put[k])
		}
	}
	// Only the kept fields and the new credential are sent: nothing derived, nothing that Kratos would refuse.
	for k := range put {
		switch k {
		case "schema_id", "state", "traits", "metadata_public", "metadata_admin", "external_id", "credentials":
		default:
			t.Errorf("unexpected field %q in the replace", k)
		}
	}
	var cred struct {
		Password struct {
			Config struct {
				Password string `json:"password"`
			} `json:"config"`
		} `json:"password"`
	}
	if err := json.Unmarshal(put["credentials"], &cred); err != nil || cred.Password.Config.Password != "a-new-password-1" {
		t.Errorf("credentials %s: %v", put["credentials"], err)
	}
}

// A refusal never carries the password or the email back to the caller or the logs; a 400 is a typed policy refusal.
func TestSetPasswordErrorsDoNotEchoTheSecret(t *testing.T) {
	status := http.StatusBadRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"schema_id":"default","state":"active","traits":{"email":"owner@x.test"}}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":400,"reason":"The password can not be used because ` + "hunter2-hunter2" + ` is too similar to the email","message":"invalid"}}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = c.SetPassword(context.Background(), "kid-1", "hunter2-hunter2")
	var rej *RejectedError
	if !errors.As(err, &rej) || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("a 400 must be a *RejectedError without the password: %v", err)
	}
	status = http.StatusInternalServerError
	err = c.SetPassword(context.Background(), "kid-1", "hunter2-hunter2")
	if err == nil || errors.As(err, &rej) || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("a 500 is a plain error without the password: %v", err)
	}
	if err := c.SetPassword(context.Background(), "", "x"); err == nil {
		t.Error("an empty id must be refused")
	}
	if err := c.SetPassword(context.Background(), "kid-1", ""); err == nil {
		t.Error("an empty password must be refused")
	}
	if pw, err := GeneratePassword(); err != nil || len(pw) < 20 {
		t.Errorf("generated password %q: %v", pw, err)
	}
}

// Neither the password, in any form, nor a readable piece of it, nor the account's email, may come out in an error. The secrets
// here are awkward on purpose: quotes, a backslash, angle brackets and non-ASCII (all escaped differently in JSON), long enough
// that a 512-byte cut falls inside them, and the Kratos text echoes them both raw and JSON-escaped.
func TestSetPasswordErrorsNeverCarryAnyFormOfTheSecret(t *testing.T) {
	password := `pa"ss\wo<rd>é-` + strings.Repeat("Xy9", 40)
	escaped, _ := json.Marshal(password)
	esc := string(escaped[1 : len(escaped)-1])
	const mail = "owner@x.test"
	fragments := []string{password, esc, password[:12], esc[:12], mail, strings.Repeat("Xy9", 8)}

	status := http.StatusBadRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"schema_id":"default","state":"active","traits":{"email":"` + mail + `"}}`))
			return
		}
		w.WriteHeader(status)
		// 500 bytes of padding first, so the password straddles the old 512-byte cut; the reason echoes it raw and escaped.
		pad := strings.Repeat("p", 500)
		reason := "the password " + password + " is too common for " + mail
		rb, _ := json.Marshal(reason)
		_, _ = w.Write([]byte(`{"pad":"` + pad + `","debug":"` + esc + `","error":{"reason":` + string(rb) + `,"message":"invalid"}}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusConflict} {
		status = code
		err := c.SetPassword(context.Background(), "kid-1", password)
		if err == nil {
			t.Fatalf("status %d: no error", code)
		}
		for _, f := range fragments {
			if strings.Contains(err.Error(), f) {
				t.Errorf("status %d: the error carries %q: %v", code, f, err)
			}
		}
		var rej *RejectedError
		if (code == http.StatusBadRequest) != errors.As(err, &rej) {
			t.Errorf("status %d: typed refusal = %v", code, errors.As(err, &rej))
		}
		if code == http.StatusBadRequest && !strings.Contains(rej.Reason, "too common") {
			t.Errorf("the useful part of the reason must survive: %q", rej.Reason)
		}
		if code != http.StatusBadRequest && !strings.Contains(err.Error(), "status=") {
			t.Errorf("a plain error names the status: %v", err)
		}
	}
}
