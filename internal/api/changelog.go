package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/changelog"
)

// Release notes, public: GET /changelog.atom (the feed) and GET /api/changelog
// (the /changelog page). Both come from the hand-written notes on the GitHub
// Releases, cached by changelog.Source; neither carries user data.

const changelogMaxAge = "public, max-age=900"

func handleChangelogAtom(src *changelog.Source) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rels, err := src.Releases(r.Context())
		if err != nil {
			http.Error(w, "release notes are unavailable right now", http.StatusServiceUnavailable)
			return
		}
		body, err := changelog.Atom(rels, time.Now())
		if err != nil {
			http.Error(w, "release notes are unavailable right now", http.StatusInternalServerError)
			return
		}
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		w.Header().Set("Cache-Control", changelogMaxAge)
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
		_, _ = w.Write(body)
	}
}

func handleChangelogJSON(src *changelog.Source) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rels, err := src.Releases(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "release notes are unavailable right now"})
			return
		}
		if rels == nil {
			rels = []changelog.Release{}
		}
		w.Header().Set("Cache-Control", changelogMaxAge)
		writeJSON(w, http.StatusOK, map[string]any{"releases": rels})
	}
}
