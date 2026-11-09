package radar

import (
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// Handler is the radar routes, mounted with its prefix stripped (/api/maps/radar):
//
//	GET /frame            -> {"frame":"2026-11-09T00:24:11Z","frame_unix":1794266651,"frames_unix":[...oldest first...],"min_zoom":3,"max_zoom":9}
//	GET /{z}/{x}/{y}.png  -> a 256x256 PNG, transparent where there is no radar (outside the US mosaics, or no echo).
//	                         Query t=<frame_unix> asks for one of the recent frames (anything else is the newest).
//
// Both answer the frame time in X-Radar-Frame (RFC 3339, UTC). An unreachable or stale feed is 503 with Retry-After: the client hides the layer for
// a while; it is never a transparent 200, which would read as "no rain". Nothing about the caller is sent upstream or logged.
func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := r.URL.Path
		if p == "/frame" {
			s.serveFrame(w, r)
			return
		}
		z, x, y, ok := ParseTilePath(p)
		if !ok {
			http.NotFound(w, r)
			return
		}
		var t int64
		if q := r.URL.Query().Get("t"); q != "" {
			n, err := strconv.ParseInt(q, 10, 64)
			if err != nil || n < 0 {
				http.Error(w, "bad frame", http.StatusBadRequest)
				return
			}
			t = n
		}
		b, frame, err := s.Tile(r.Context(), z, x, y, t)
		if err != nil {
			unavailable(w, err)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "image/png")
		h.Set("X-Radar-Frame", frame.UTC().Format(time.RFC3339))
		// A tile of a named frame never changes; "the newest" changes every couple of minutes.
		if t != 0 {
			h.Set("Cache-Control", "private, max-age=600")
		} else {
			h.Set("Cache-Control", "private, max-age=60")
		}
		h.Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(b)
	})
}

func (s *Service) serveFrame(w http.ResponseWriter, r *http.Request) {
	fr, err := s.Frames(r.Context())
	if err != nil {
		unavailable(w, err)
		return
	}
	unix := make([]int64, len(fr.Recent))
	for i, t := range fr.Recent {
		unix[i] = t.Unix()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.Header().Set("X-Radar-Frame", fr.Latest.UTC().Format(time.RFC3339))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"frame": fr.Latest.UTC().Format(time.RFC3339), "frame_unix": fr.Latest.Unix(), "frames_unix": unix,
		"min_zoom": ClientMinZoom, "max_zoom": ClientMaxZoom,
	})
}

func unavailable(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnavailable) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "radar is not available right now", http.StatusServiceUnavailable)
		return
	}
	http.Error(w, "bad tile", http.StatusBadRequest)
}

// ParseTilePath parses "/z/x/y.png" strictly: no leading zeros, no extra segments or characters, x and y inside the zoom's grid, z up to MaxZoom.
func ParseTilePath(p string) (z, x, y int, ok bool) {
	if path.Clean(p) != p || !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, ".png") {
		return 0, 0, 0, false
	}
	parts := strings.Split(strings.TrimSuffix(p[1:], ".png"), "/")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var n [3]int
	for i, s := range parts {
		if s == "" || len(s) > 8 || (len(s) > 1 && s[0] == '0') {
			return 0, 0, 0, false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return 0, 0, 0, false
			}
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, 0, 0, false
		}
		n[i] = v
	}
	z, x, y = n[0], n[1], n[2]
	if z > MaxZoom || x >= 1<<z || y >= 1<<z {
		return 0, 0, 0, false
	}
	return z, x, y, true
}
