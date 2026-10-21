package api

import (
	"encoding/json"
	"html/template"
	"net/http"
	"regexp"
)

// appIDRE is an Apple app ID: a 10-character Team ID, a dot, a bundle ID.
var appIDRE = regexp.MustCompile(`^[A-Z0-9]{10}\.[A-Za-z0-9.-]+$`)

// ValidAppID reports whether id looks like "TEAMID.bundle.id".
func ValidAppID(id string) bool { return appIDRE.MatchString(id) }

// aasaDoc is /.well-known/apple-app-site-association. applinks makes
// https://<host>/app/callback a Universal Link into the iPhone app (the
// sign-in callback, so only our app receives the one-time code);
// webcredentials is what ASWebAuthenticationSession requires for an https
// callback on this host.
type aasaDoc struct {
	AppLinks       aasaAppLinks       `json:"applinks"`
	WebCredentials aasaWebCredentials `json:"webcredentials"`
}

type aasaAppLinks struct {
	Details []aasaDetail `json:"details"`
}

type aasaDetail struct {
	AppIDs     []string        `json:"appIDs"`
	Components []aasaComponent `json:"components"`
}

type aasaComponent struct {
	Path    string `json:"/"`
	Comment string `json:"comment,omitempty"`
}

type aasaWebCredentials struct {
	Apps []string `json:"apps"`
}

// handleAASA serves the association file for appIDs. Apple's CDN fetches it
// over https, unauthenticated, without redirects, as application/json.
func handleAASA(appIDs []string) http.HandlerFunc {
	doc := aasaDoc{
		AppLinks: aasaAppLinks{Details: []aasaDetail{{
			AppIDs:     appIDs,
			Components: []aasaComponent{{Path: appCallbackPath, Comment: "iPhone app sign-in callback"}},
		}}},
		WebCredentials: aasaWebCredentials{Apps: appIDs},
	}
	body, _ := json.Marshal(doc)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(body)
	}
}

// appCallbackPath is the iPhone app's Universal Link callback on this host
// (RIVOLT_APP_CALLBACK_URL = https://<host>/app/callback).
const appCallbackPath = "/app/callback"

// handleAppCallbackPage is what a browser shows if the Universal Link didn't
// open the app (it isn't installed, or the user long-pressed). It never
// repeats the code from the URL, and sends no Referer anywhere.
func handleAppCallbackPage() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		_ = appCallbackPage.Execute(w, nil)
	}
}

var appCallbackPage = template.Must(template.New("callback").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Return to the Rivolt app</title>
<style>body{font:17px -apple-system,system-ui,sans-serif;max-width:28rem;margin:3rem auto;padding:0 1rem}</style>
</head><body>
<h1>Return to the Rivolt app</h1>
<p>This page finishes signing in to the Rivolt iPhone app. If the app didn't open, open it on this phone and tap Sign in again.</p>
</body></html>`))
