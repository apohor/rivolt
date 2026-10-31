package maps

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The shipped files are exactly the ones taken from protomaps/basemaps-assets at the pinned commit (see glyphdata/README.md): a changed or
// replaced file fails here.
var glyphSHA256 = map[string]string{
	"Noto Sans Italic/0-255":    "43edfca91c285ba1226f09d5e74d68e1473a088c517f02a5212ff6ccb10037dc",
	"Noto Sans Italic/256-511":  "a6f9f6574c86a4a28aba630ca1017857ceadd2370ed32a1293c35f819ab9bd60",
	"Noto Sans Medium/0-255":    "ba2f0118dd024e3041b158e5f9eb49bc0a658019f53f458e9f5c0b8efcd79b91",
	"Noto Sans Medium/256-511":  "d5e801a1a5b1d409d3298c3a1e1ca76328e2314a751078833a618620e8e66e4d",
	"Noto Sans Regular/0-255":   "62c6d49b15fa836eb6aa45e259c7ca6762f44b011b09e47776efbe4a6db1b397",
	"Noto Sans Regular/256-511": "2eca7561f9f566bcacfda5dd04fb5880baec1328ec0f5484678289a13994de8a",
}

func TestShippedGlyphsArePinned(t *testing.T) {
	if len(glyphFiles) != len(glyphSHA256) || len(glyphFiles) != len(GlyphFonts)*len(GlyphRanges) {
		t.Fatalf("%d files, %d pinned", len(glyphFiles), len(glyphSHA256))
	}
	for k, want := range glyphSHA256 {
		f, ok := glyphFiles[k]
		if !ok {
			t.Errorf("%s is not shipped", k)
			continue
		}
		sum := sha256.Sum256(f.data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: sha256 %s, want %s", k, got, want)
		}
		if len(f.data) < 10_000 {
			t.Errorf("%s is %d bytes: not a glyph range", k, len(f.data))
		}
	}
	if b, err := glyphFS.ReadFile("glyphdata/OFL.txt"); err != nil || !strings.Contains(string(b), "SIL OPEN FONT LICENSE Version 1.1") {
		t.Errorf("the font licence is not shipped with the fonts: %v", err)
	}
}

func TestGlyphPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/Noto Sans Regular/0-255.pbf":                     true,
		"/Noto Sans Medium/256-511.pbf":                    true,
		"/Noto Sans Italic/0-255.pbf":                      true,
		"/Noto Sans Regular/512-767.pbf":                   false, // a range that is not shipped
		"/Noto Sans Bold/0-255.pbf":                        false, // a font that is not shipped
		"/Noto Sans Regular,Noto Sans Italic/0-255.pbf":    false, // a comma stack
		"/Noto Sans Regular/0-255":                         false,
		"/Noto Sans Regular/0-255.png":                     false,
		"/Noto Sans Regular/../Noto Sans Medium/0-255.pbf": false,
		"/../glyphdata/OFL.txt":                            false,
		"//Noto Sans Regular/0-255.pbf":                    false,
		"/Noto Sans Regular/0-255.pbf/":                    false,
		"/Noto Sans Regular/0-255.pbf/x":                   false,
		"/noto sans regular/0-255.pbf":                     false, // exact name only
		"/Noto Sans Regular\\0-255.pbf":                    false,
		"/Noto%20Sans%20Regular/0-255.pbf":                 false, // the router decodes first; an encoded name arriving here is not a font
		"":                                                 false,
		"/":                                                false,
	} {
		if _, got := GlyphPath(p); got != want {
			t.Errorf("GlyphPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestGlyphHandler(t *testing.T) {
	h := GlyphHandler()
	do := func(method, p, inm string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/x", nil)
		req.URL.Path = p
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := do(http.MethodGet, "/Noto Sans Regular/0-255.pbf", "")
	f := glyphFiles["Noto Sans Regular/0-255"]
	if rec.Code != 200 || rec.Body.Len() != len(f.data) || rec.Header().Get("Content-Type") != "application/x-protobuf" ||
		rec.Header().Get("Cache-Control") != GlyphMaxAge || rec.Header().Get("ETag") != f.etag || rec.Header().Get("Content-Length") != strconv.Itoa(len(f.data)) {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if rec := do(http.MethodGet, "/Noto Sans Regular/0-255.pbf", f.etag); rec.Code != 304 || rec.Body.Len() != 0 {
		t.Errorf("revalidation: %d %d", rec.Code, rec.Body.Len())
	}
	if rec := do(http.MethodGet, "/Noto Sans Regular/0-255.pbf", `"other"`); rec.Code != 200 {
		t.Errorf("a different ETag: %d", rec.Code)
	}
	if rec := do(http.MethodHead, "/Noto Sans Medium/256-511.pbf", ""); rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: %d %d", rec.Code, rec.Body.Len())
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := do(m, "/Noto Sans Regular/0-255.pbf", ""); rec.Code != 404 {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
	if rec := do(http.MethodGet, "/Noto Sans Regular/512-767.pbf", ""); rec.Code != 404 {
		t.Errorf("an unshipped range: %d", rec.Code)
	}
	if GlyphSize("/Noto Sans Regular/0-255.pbf") != len(f.data) || GlyphSize("/nope") != 0 {
		t.Error("GlyphSize")
	}
}
