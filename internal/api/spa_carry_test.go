package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/apohor/rivolt/internal/web"
)

// During a roll a browser can ask a pod for a chunk of the other build. A chunk only the carry directory has is served, immutable like any
// hashed asset; one that is in neither is the same uncacheable 404 as before.
func TestSPAServesAChunkFromTheCarryDirAndStill404sTheRest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "old-BBBBBBBB.js"), []byte("old chunk"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsys, _ := web.WithCarry(fstest.MapFS{"index.html": {Data: []byte("shell")}, "assets/new-AAAAAAAA.js": {Data: []byte("new chunk")}}, dir)
	h := spaHandler(fsys)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	if rec := get("/assets/old-BBBBBBBB.js"); rec.Code != http.StatusOK || rec.Body.String() != "old chunk" || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("carry-only chunk: %d %q cache %q, want 200 with the immutable header", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	if rec := get("/assets/new-AAAAAAAA.js"); rec.Code != http.StatusOK || rec.Body.String() != "new chunk" {
		t.Errorf("own chunk: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/assets/gone-CCCCCCCC.js"); rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("a chunk in neither: %d cache %q, want 404 no-store", rec.Code, rec.Header().Get("Cache-Control"))
	}
}
