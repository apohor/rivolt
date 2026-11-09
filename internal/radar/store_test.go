package radar

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// rawFile is a gzip file for the synthetic grid with the origin cell set to dbz.
func rawFile(t *testing.T, dbz float64, snow bool) []byte {
	t.Helper()
	return synthFile(t, func(d, s []byte) {
		d[10*20+10] = enc(dbz)
		if snow {
			s[10*20+10] = 1
		}
	})
}

func TestStoreAt(t *testing.T) {
	ct := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	files := map[int][]byte{}
	for h, c := range map[int]struct {
		dbz  float64
		snow bool
	}{3: {5, false}, 4: {35, false}, 5: {35, true}} {
		files[h] = rawFile(t, c.dbz, c.snow)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest.json" {
			fmt.Fprintf(w, `{"v":1,"cycles":[{"cycle":"2026110818","cycle_time":%q,"hours":[3,4,5]}]}`, ct.Format(time.RFC3339))
			return
		}
		var h int
		if _, err := fmt.Sscanf(r.URL.Path, "/conus/2026110818/f%d.bin.gz", &h); err != nil || files[h] == nil {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_, _ = w.Write(files[h])
	}))
	defer srv.Close()
	s := NewStore(srv.URL, nil)
	now := ct.Add(4 * time.Hour)
	s.now = func() time.Time { return now }
	ctx := context.Background()

	r, ok := s.At(ctx, 38.5, -97.5, ct.Add(3*time.Hour))
	if !ok || r.Level != Dry {
		t.Fatalf("exact hour 3: %+v %v", r, ok)
	}
	r, ok = s.At(ctx, 38.5, -97.5, ct.Add(3*time.Hour+30*time.Minute)) // hours 3 and 4: the strongest wins
	if !ok || r.Level != Rain || r.DBZ != 35 {
		t.Fatalf("between 3 and 4: %+v %v", r, ok)
	}
	r, ok = s.At(ctx, 38.5, -97.5, ct.Add(4*time.Hour+30*time.Minute))
	if !ok || r.Level != Snow {
		t.Fatalf("between 4 and 5: %+v %v", r, ok)
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("grids fetched %d times, want 3 (cache)", n)
	}
	if _, ok := s.At(ctx, 38.5, -97.5, ct.Add(6*time.Hour)); ok {
		t.Fatal("past the horizon the field is absent")
	}
	if _, ok := s.At(ctx, 38.5, -97.5, ct.Add(-time.Hour)); ok {
		t.Fatal("before the cycle is absent")
	}
	if _, ok := s.At(ctx, 47.6, -122.3, ct.Add(4*time.Hour)); ok {
		t.Fatal("outside the grid is absent")
	}
	now = ct.Add(MaxCycleAge + time.Minute)
	s.manAt = time.Time{}
	if _, ok := s.At(ctx, 38.5, -97.5, ct.Add(4*time.Hour)); ok {
		t.Fatal("a stale cycle is absent")
	}
}

func TestStoreCapEvicts(t *testing.T) {
	f := rawFile(t, 30, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(f) }))
	defer srv.Close()
	s := NewStore(srv.URL, nil)
	one, _ := Decode(bytes.NewReader(f))
	s.limit = one.Bytes()*2 + 1
	for h := 0; h < 5; h++ {
		if _, err := s.grid(context.Background(), "c", h); err != nil {
			t.Fatal(err)
		}
	}
	if s.lru.Len() != 2 || s.CachedBytes() > s.limit {
		t.Fatalf("len %d bytes %d limit %d", s.lru.Len(), s.CachedBytes(), s.limit)
	}
}

func TestNilStoreIsOff(t *testing.T) {
	var s *Store
	if _, ok := s.At(context.Background(), 1, 1, time.Now()); ok {
		t.Fatal("nil store must report absent")
	}
	if NewStore("", nil) != nil {
		t.Fatal("empty URL means off")
	}
}
