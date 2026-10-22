package maps

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestAllowlists(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	cases := []struct {
		name   string
		h      func(http.Handler) http.Handler
		method string
		path   string
		want   int
	}{
		{"route", AllowValhalla, "POST", "/route", 204},
		{"trace", AllowValhalla, "POST", "/trace_route", 204},
		{"route GET", AllowValhalla, "GET", "/route", 404},
		{"isochrone", AllowValhalla, "POST", "/isochrone", 404},
		{"matrix", AllowValhalla, "POST", "/sources_to_targets", 404},
		{"expansion", AllowValhalla, "POST", "/expansion", 404},
		{"status", AllowValhalla, "GET", "/status", 404},
		{"traversal", AllowValhalla, "POST", "/route/../isochrone", 404},
		{"pmtiles", AllowTiles, "GET", "/us.pmtiles", 204},
		{"pmtiles head", AllowTiles, "HEAD", "/chargers.pmtiles", 204},
		{"pmtiles post", AllowTiles, "POST", "/us.pmtiles", 404},
		{"other file", AllowTiles, "GET", "/index.html", 404},
		{"listing", AllowTiles, "GET", "/", 404},
		{"nested", AllowTiles, "GET", "/a/us.pmtiles", 404},
		{"dotdot", AllowTiles, "GET", "/../etc/x.pmtiles", 404},
		{"bare suffix", AllowTiles, "GET", "/.pmtiles", 404},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://x"+c.path, nil)
		req.Header.Set("Range", "bytes=0-16383") // a tile GET names one bounded range
		rec := httptest.NewRecorder()
		c.h(ok).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, c.path, rec.Code, c.want)
		}
	}
}

// fakeTileServer answers the way a static file server does: a Range with no (or a
// matching) validator is a 206 of exactly that range, but an If-Range that does not
// match makes it ignore the Range and send the whole file with a 200.
func fakeTileServer(file []byte, seen *http.Header) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Clone()
		}
		rng := r.Header.Get("Range")
		if rng == "" || r.Header.Get("If-Range") != "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(file)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(file)
			return
		}
		var a, b int
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &a, &b); err != nil || a > b || b >= len(file) {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(file)))
		w.Header().Set("Content-Length", strconv.Itoa(b-a+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(file[a : b+1])
	})
}

// A conditional header can not turn a range read into the whole file: none reaches the
// tile server, so the read is the 206 of exactly the bytes asked for.
func TestAllowTilesStripsConditionalHeaders(t *testing.T) {
	file := bytes.Repeat([]byte("0123456789"), 1000)
	var seen http.Header
	h := AllowTiles(fakeTileServer(file, &seen))
	for _, hdr := range []string{"If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		req := httptest.NewRequest(http.MethodGet, "/us.pmtiles", nil)
		req.Header.Set("Range", "bytes=0-9")
		req.Header.Set(hdr, `"bogus"`)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent || rec.Body.Len() != 10 {
			t.Errorf("%s: %d with %d bytes, want a 206 of 10", hdr, rec.Code, rec.Body.Len())
		}
		if seen.Get(hdr) != "" {
			t.Errorf("%s reached the tile server", hdr)
		}
	}
}

// An upstream that answers a GET with a 200 (the whole file) or with more than a tile
// read may carry is refused with a 502 and none of the body is sent; a HEAD, a 206
// and a 416 pass.
func TestAllowTilesRefusesWholeFileAnswers(t *testing.T) {
	big := bytes.Repeat([]byte("x"), MaxTileResponse+1)
	always200 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(big)))
		_, _ = w.Write(big)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/us.pmtiles", nil)
	req.Header.Set("Range", "bytes=0-9")
	AllowTiles(always200).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || rec.Body.Len() > 100 || rec.Header().Get("Content-Length") == strconv.Itoa(len(big)) {
		t.Errorf("a 200 to a range: %d, %d body bytes", rec.Code, rec.Body.Len())
	}
	// A 206 whose Content-Length is over the cap is refused too.
	over := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(MaxTileResponse+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(big)
	})
	rec = httptest.NewRecorder()
	AllowTiles(over).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || rec.Body.Len() > 100 {
		t.Errorf("an oversized 206: %d, %d body bytes", rec.Code, rec.Body.Len())
	}
	// No Range at all, an open-ended one, one past the cap, several, and a malformed
	// one never reach the tile server.
	for rng, want := range map[string]int{
		"": http.StatusBadRequest, "bytes=0-": http.StatusBadRequest, "bytes=0-2097152": http.StatusRequestedRangeNotSatisfiable,
		"bytes=-9999999": http.StatusRequestedRangeNotSatisfiable, "bytes=0-1,5-9": http.StatusBadRequest,
		"bytes=9-1": http.StatusBadRequest, "items=0-1": http.StatusBadRequest,
	} {
		reached := false
		spy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })
		rec = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/us.pmtiles", nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		AllowTiles(spy).ServeHTTP(rec, req)
		if rec.Code != want || reached {
			t.Errorf("Range %q: %d (reached upstream: %v), want %d before the upstream", rng, rec.Code, reached, want)
		}
	}
	// The boundary: exactly MaxTileResponse and a suffix read are fine.
	for _, rng := range []string{"bytes=0-2097151", "bytes=-1024", "bytes=1000-1999"} {
		reached := false
		spy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(http.StatusPartialContent) })
		rec = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/us.pmtiles", nil)
		req.Header.Set("Range", rng)
		AllowTiles(spy).ServeHTTP(rec, req)
		if !reached || rec.Code != http.StatusPartialContent {
			t.Errorf("Range %q: %d, reached=%v, want let through", rng, rec.Code, reached)
		}
	}
	// HEAD (no body), a good 206 and a 416 pass.
	rec = httptest.NewRecorder()
	AllowTiles(always200).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/us.pmtiles", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	good := httptest.NewRequest(http.MethodGet, "/us.pmtiles", nil)
	good.Header.Set("Range", "bytes=5-14")
	AllowTiles(fakeTileServer(big, nil)).ServeHTTP(rec, good)
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 10 {
		t.Errorf("good range: %d with %d bytes", rec.Code, rec.Body.Len())
	}
	rec = httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodGet, "/us.pmtiles", nil)
	bad.Header.Set("Range", "bytes=9999999999-9999999999999")
	AllowTiles(fakeTileServer(big, nil)).ServeHTTP(rec, bad)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("unsatisfiable: %d", rec.Code)
	}
}

// When the guard refuses an upstream answer it stops reading it: a tile server that
// streams a huge body is cut off after a few buffers, not drained into the void.
func TestAllowTilesStopsReadingARefusedBody(t *testing.T) {
	const total = 400 << 20
	var sent atomic.Int64
	done := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		w.Header().Set("Content-Length", strconv.Itoa(total))
		w.WriteHeader(http.StatusOK) // a 200 to a range read, the whole archive
		chunk := make([]byte, 64<<10)
		for sent.Load() < total {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	proxy, err := NewProxy(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(AllowTiles(proxy))
	defer front.Close()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/us.pmtiles", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || len(body) > 100 {
		t.Errorf("client got %d with %d bytes, want a 502 with a short message", resp.StatusCode, len(body))
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream was still being read 10 s after the refusal")
	}
	if got := sent.Load(); got > 64<<20 {
		t.Errorf("the tile server sent %d MB of a %d MB body before it was cut off, want a few MB at most", got>>20, total>>20)
	}
}
