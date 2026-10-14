package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// Kratos errors can echo the email (query string in a *url.Error, or the
// API error body); logs must not carry it.
func TestRedactIdentifier(t *testing.T) {
	email := "Gone+tag@Example.com"
	_, err := http.Get("http://127.0.0.1:1/admin/identities?" + url.Values{"credentials_identifier": {email}}.Encode())
	if err == nil {
		t.Skip("expected a transport error")
	}
	wrapped := errors.New("kratos list: " + err.Error() + ` body={"identifier":"gone+tag@example.com"}`)
	got := redactIdentifier(wrapped.Error(), email)
	for _, leak := range []string{"gone+tag@example.com", url.QueryEscape("gone+tag@example.com")} {
		if strings.Contains(strings.ToLower(got), strings.ToLower(leak)) {
			t.Errorf("redacted text still contains %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Errorf("nothing redacted: %s", got)
	}
	long := redactIdentifier(strings.Repeat("é", 1000), "x@y.z")
	if len(long) > maxLoggedErr+len("…") || !utf8.ValidString(long) {
		t.Errorf("truncation: %d bytes, valid UTF-8 %v", len(long), utf8.ValidString(long))
	}
}
