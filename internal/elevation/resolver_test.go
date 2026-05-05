package elevation

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// makeTerrariumPNG synthesises a 256x256 Terrarium tile where every
// pixel encodes the same elevation `meters`. Reverses the Terrarium
// formula:  height = R*256 + G + B/256 - 32768.
func makeTerrariumPNG(t *testing.T, meters int) []byte {
	t.Helper()
	enc := meters + 32768 // 0..65535
	r := uint8((enc >> 8) & 0xff)
	g := uint8(enc & 0xff)
	img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			i := y*img.Stride + x*4
			img.Pix[i+0] = r
			img.Pix[i+1] = g
			img.Pix[i+2] = 0
			img.Pix[i+3] = 0xff
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

// waitFor spins until cond returns true or the deadline passes. Used
// because tile fetches happen on a background goroutine and Lookup
// returns ok=false until the tile lands in the cache.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// TestDiskCachePersistsAcrossResolvers covers the core "pod restart
// shouldn't re-fetch tiles" guarantee: a first Resolver writes a tile
// to disk via HTTP, a second Resolver pointed at a blackholed upstream
// reads the same coordinate purely from disk.
func TestDiskCachePersistsAcrossResolvers(t *testing.T) {
	dir := t.TempDir()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(makeTerrariumPNG(t, 1234))
	}))
	defer srv.Close()

	// Round-trip through the first resolver: cold cache, expect one
	// HTTP fetch and the tile to land both in mem and on disk.
	r1 := New(Config{
		TileURL:  srv.URL + "/{z}/{x}/{y}.png",
		CacheDir: dir,
	})
	// First Lookup misses (kicks off async fetch), eventually hits.
	r1.Lookup(30.0, -97.0)
	waitFor(t, func() bool {
		v, ok := r1.Lookup(30.0, -97.0)
		return ok && int(v) == 1234
	}, "first resolver to warm")
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 upstream fetch on cold start, got %d", got)
	}

	// Second resolver pointed at a server that always 500s — proves
	// the tile is being served from disk, not refetched.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer bad.Close()

	r2 := New(Config{
		TileURL:  bad.URL + "/{z}/{x}/{y}.png",
		CacheDir: dir,
	})
	r2.Lookup(30.0, -97.0)
	waitFor(t, func() bool {
		v, ok := r2.Lookup(30.0, -97.0)
		return ok && int(v) == 1234
	}, "second resolver to warm from disk")

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected disk-only second resolver, but upstream was hit (total=%d)", got)
	}
}

// TestDiskCacheCorruptFileIsRecovered verifies that a half-written /
// truncated tile on disk doesn't permanently poison the cache: read
// fails, file is unlinked, HTTP fetch rewrites a clean copy.
func TestDiskCacheCorruptFileIsRecovered(t *testing.T) {
	dir := t.TempDir()

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.Copy(w, bytes.NewReader(makeTerrariumPNG(t, 555)))
	}))
	defer srv.Close()

	r := New(Config{
		TileURL:  srv.URL + "/{z}/{x}/{y}.png",
		CacheDir: dir,
	})

	// Pre-populate disk with garbage at the path the resolver will
	// look at for (lat=30, lon=-97) at z=12.
	xtile, ytile, _, _ := tileCoords(30.0, -97.0, DefaultZoom)
	corruptPath := filepath.Join(
		dir,
		fileName(DefaultZoom),
		fileName(xtile),
		fileName(ytile)+".png",
	)
	if err := os.MkdirAll(filepath.Dir(corruptPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(corruptPath, []byte("not a png"), 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	r.Lookup(30.0, -97.0)
	waitFor(t, func() bool {
		v, ok := r.Lookup(30.0, -97.0)
		return ok && int(v) == 555
	}, "resolver to recover from corrupt disk file")

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected exactly one upstream refetch after corrupt disk hit, got %d", got)
	}
	// The on-disk copy should now be valid (so the next pod restart
	// is again disk-only).
	data, err := os.ReadFile(corruptPath)
	if err != nil {
		t.Fatalf("read repaired tile: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		t.Fatalf("repaired tile is not a valid PNG: %v", err)
	}
}

// fileName mirrors the digit-only path components diskPath uses; kept
// local to the test so it doesn't depend on internal helpers.
func fileName(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	x := n
	if x < 0 {
		digits = append(digits, '-')
		x = -x
	}
	rev := []byte{}
	for x > 0 {
		rev = append(rev, byte('0'+x%10))
		x /= 10
	}
	for i := len(rev) - 1; i >= 0; i-- {
		digits = append(digits, rev[i])
	}
	return string(digits)
}
