package logging

import (
	"errors"
	"strings"
	"testing"
)

func TestScrubEmails(t *testing.T) {
	for in, want := range map[string]string{
		"send to john.smith+qa@gmail.com failed":           "send to <email> failed",
		"duplicate key (email)=(a.b+c@example.com) exists": "duplicate key (email)=(<email>) exists",
		"GET /x?email=a.b%2Bc%40example.com: dial tcp":     "GET /x?email=<email>: dial tcp",
		"GET /x?email=john+qa%40gmail.com&p=1":             "GET /x?email=<email>&p=1",
		"bad A@Example.COM":                                "bad <email>",
		"o'brien@x.com refused":                            "<email> refused",
		"no address here":                                  "no address here",
	} {
		if got := ScrubEmails(in); got != want {
			t.Errorf("ScrubEmails(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ScrubErr(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if got := ScrubErr(errors.New("x@y.io")); got != "<email>" {
		t.Errorf("ScrubErr = %q", got)
	}
}

func TestIDHash(t *testing.T) {
	a, b := IDHash("u@x.io"), IDHash("u@x.io")
	if a != b || len(a) != 12 || strings.Contains(a, "@") || a == IDHash("v@x.io") {
		t.Errorf("IDHash: %q %q", a, b)
	}
}
