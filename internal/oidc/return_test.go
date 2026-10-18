package oidc

import "testing"

func TestSafeReturnPath(t *testing.T) {
	for _, p := range []string{
		"/",
		"/dashboard",
		"/admin/users?tab=audit",
		"/trips/42#map",
		"/a/b%5Cc", // an encoded backslash stays literal in a Location
		"/redirect?to=https://example.com",
		"/@not-a-host", // a path, not userinfo
	} {
		if !safeReturnPath(p) {
			t.Errorf("%q should be accepted", p)
		}
	}
	for name, p := range map[string]string{
		"empty":                    "",
		"absolute url":             "https://evil.example/",
		"scheme-relative":          "//evil.example",
		"scheme-relative path":     "//evil.example/x",
		"no leading slash":         "evil.example",
		"backslash first":          `/\evil.example`,
		"backslash later":          `/a\b`,
		"double backslash":         `/\\evil.example`,
		"tab after slash":          "/\t/evil.example",
		"tab in the middle":        "/a\tb",
		"newline":                  "/\n/evil.example",
		"carriage return":          "/\r/evil.example",
		"nul":                      "/a\x00b",
		"del":                      "/a\x7fb",
		"scheme in path position":  "javascript:alert(1)",
		"leading space then slash": " /x",
	} {
		got := safeReturnPath(p)
		if got {
			t.Errorf("%s %q should be rejected", name, p)
		}
	}
}
