// Package iosapp is the link to Rivolt's iPhone app: one setting, RIVOLT_IOS_APP_URL, that
// every surface (the Settings row, the share card, the store banner, /app, the welcome and
// statement emails) reads. Empty, or anything but a plain App Store or TestFlight link,
// means the app is not promoted anywhere.
package iosapp

import (
	"net/url"
	"os"
	"regexp"
	"strings"
)

// EnvURL is the setting.
const EnvURL = "RIVOLT_IOS_APP_URL"

// Line is the one plain sentence the emails carry; the link follows it.
const Line = "Rivolt is also an iPhone app: "

var appStoreID = regexp.MustCompile(`/id(\d{6,12})(?:/|$)`)

// URL is the configured link, or "" when unset or not acceptable. Acceptable: https, host
// apps.apple.com or testflight.apple.com, no query, fragment or credentials (a link with
// tracking parameters or Apple tokens in the query is refused, and so is any other host,
// which also keeps /app from being an open redirect).
func URL() string { return Clean(os.Getenv(EnvURL)) }

// Clean validates a link as URL does.
func Clean(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return ""
	}
	if h := strings.ToLower(u.Hostname()); (h != "apps.apple.com" && h != "testflight.apple.com") || u.Port() != "" {
		return ""
	}
	if len(strings.Trim(u.Path, "/")) == 0 {
		return ""
	}
	return u.String()
}

// AppStoreID is the numeric app id of an apps.apple.com link ("" for TestFlight or anything
// else), for the Smart App Banner.
func AppStoreID(link string) string {
	u, err := url.Parse(Clean(link))
	if err != nil || u == nil || strings.ToLower(u.Hostname()) != "apps.apple.com" {
		return ""
	}
	if m := appStoreID.FindStringSubmatch(u.Path); m != nil {
		return m[1]
	}
	return ""
}
