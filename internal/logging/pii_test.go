package logging

import (
	"errors"
	"strings"
	"testing"
)

func TestScrubEmails(t *testing.T) {
	for _, in := range []string{
		`duplicate key (email)=(a.b+c@example.com) exists`,
		`GET /x?email=a.b%2Bc%40example.com: dial tcp`,
		`bad A@Example.COM`,
	} {
		if out := ScrubEmails(in); strings.Contains(out, "@") || strings.Contains(strings.ToLower(out), "example") {
			t.Errorf("not scrubbed: %q -> %q", in, out)
		}
	}
	if got := ScrubErr(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if strings.Contains(ScrubErr(errors.New("x@y.io")), "@") {
		t.Error("ScrubErr leaked")
	}
}

func TestIDHash(t *testing.T) {
	a, b := IDHash("u@x.io"), IDHash("u@x.io")
	if a != b || len(a) != 12 || strings.Contains(a, "@") || a == IDHash("v@x.io") {
		t.Errorf("IDHash: %q %q", a, b)
	}
}
