package maps

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// Map label glyphs, embedded: the web map's labels need font glyph ranges (.pbf), and fetching them from a CDN would be a third-party request
// per label range. The ones the style uses are in glyphdata/ (see its README for provenance and licence) and served from here.

//go:embed glyphdata/fonts glyphdata/OFL.txt
var glyphFS embed.FS

// GlyphFonts and GlyphRanges are exactly what is shipped; anything else is a 404.
var (
	GlyphFonts  = []string{"Noto Sans Regular", "Noto Sans Medium", "Noto Sans Italic"}
	GlyphRanges = []string{"0-255", "256-511"}
)

// GlyphMaxAge is how long a glyph file may be cached: the files never change under a URL (a new set would be a new release), and the ETag
// lets a client revalidate cheaply after that.
const GlyphMaxAge = "public, max-age=604800"

type glyphFile struct {
	data []byte
	etag string
}

var glyphFiles = loadGlyphs()

func loadGlyphs() map[string]glyphFile {
	out := map[string]glyphFile{}
	for _, f := range GlyphFonts {
		for _, r := range GlyphRanges {
			b, err := fs.ReadFile(glyphFS, path.Join("glyphdata/fonts", f, r+".pbf"))
			if err != nil {
				panic("maps: glyph file missing from the binary: " + f + "/" + r + ": " + err.Error())
			}
			sum := sha256.Sum256(b)
			out[f+"/"+r] = glyphFile{data: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		}
	}
	return out
}

// GlyphPath parses "/{fontstack}/{range}.pbf" for a shipped font and range, and returns the file's key. The font stack is the decoded path
// segment (a single font; a comma-joined stack is not served).
func GlyphPath(p string) (key string, ok bool) {
	if path.Clean(p) != p || !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, ".pbf") {
		return "", false
	}
	parts := strings.Split(strings.TrimSuffix(p[1:], ".pbf"), "/")
	if len(parts) != 2 {
		return "", false
	}
	k := parts[0] + "/" + parts[1]
	if _, ok := glyphFiles[k]; !ok {
		return "", false
	}
	return k, true
}

// GlyphSize is the size in bytes of the file at a valid glyph path (0 when it is not one).
func GlyphSize(p string) int {
	if k, ok := GlyphPath(p); ok {
		return len(glyphFiles[k].data)
	}
	return 0
}

// GlyphHandler serves a glyph range from memory, with the path after /api/maps/glyphs. GET and HEAD only, an exact shipped font and range
// only, an ETag and a week of caching.
func GlyphHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		k, ok := GlyphPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		f := glyphFiles[k]
		h := w.Header()
		h.Set("Content-Type", "application/x-protobuf")
		h.Set("Cache-Control", GlyphMaxAge)
		h.Set("ETag", f.etag)
		h.Set("X-Content-Type-Options", "nosniff")
		if r.Header.Get("If-None-Match") == f.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Length", strconv.Itoa(len(f.data)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(f.data)
	})
}
