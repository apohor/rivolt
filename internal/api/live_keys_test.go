package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/livekeys"
	"github.com/apohor/rivolt/internal/radar"
)

func liveKeysFile(t *testing.T, body string) (string, func(string)) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keys")
	w := func(b string) {
		if err := os.WriteFile(p+".tmp", []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(p+".tmp", p); err != nil {
			t.Fatal(err)
		}
	}
	w(body)
	return p, w
}

func configOf(t *testing.T, h http.Handler) (keys []string, radarMax int, cc string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/config = %d", rec.Code)
	}
	var body struct {
		Features struct {
			DesignPreview []string `json:"design_preview"`
		} `json:"features"`
		Tiles struct {
			RadarMaxZoom int `json:"radar_maxzoom"`
		} `json:"tiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Features.DesignPreview, body.Tiles.RadarMaxZoom, rec.Header().Get("Cache-Control")
}

// The real router: /api/config follows the list while the process runs, with no new router and no restart. A bad file keeps the last good list.
func TestRouterConfigFollowsLiveKeys(t *testing.T) {
	p, write := liveKeysFile(t, "design_preview=theme_switch\n")
	ks, err := livekeys.New(p, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := contractRouterWith(t, true, func(d *Deps) { d.DesignKeys = ks.Keys })

	step := func(name string, want []string) {
		t.Helper()
		got, _, cc := configOf(t, h.(http.Handler))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: design_preview = %v, want %v", name, got, want)
		}
		if cc != "no-store" {
			t.Fatalf("%s: Cache-Control = %q, want no-store", name, cc)
		}
	}
	step("boot", []string{"theme_switch"})
	write("design_preview=theme_switch,now_v2\n")
	ks.Reload()
	step("key on", []string{"theme_switch", "now_v2"})
	write("design_preview=theme_switch,Bad Key\n")
	ks.Reload()
	step("bad file keeps the last good list", []string{"theme_switch", "now_v2"})
	write("design_preview=\n")
	ks.Reload()
	step("every key off", []string{})
}

// Radar's max zoom in /api/config and the tile service's smoothing both follow the list.
func TestRadarSmoothFollowsLiveKeys(t *testing.T) {
	p, write := liveKeysFile(t, "design_preview=\n")
	ks, _ := livekeys.New(p, "", nil)
	h := handleConfig(false, false, false, false, nil, nil, configExtras{Radar: true, DesignKeys: ks.Keys})
	_, off, _ := configOf(t, h)
	write("design_preview=" + radar.SmoothKey + "\n")
	ks.Reload()
	_, on, _ := configOf(t, h)
	if off != radar.ClientMaxZoom || on != radar.SmoothMaxZoom {
		t.Fatalf("radar max zoom off=%d on=%d", off, on)
	}
	svc := radar.New(radar.Config{SmoothFn: func() bool { return ks.Has(radar.SmoothKey) }})
	if svc.ClientMaxZoom() != radar.SmoothMaxZoom {
		t.Fatal("service ignores the live key")
	}
	write("design_preview=\n")
	ks.Reload()
	if svc.ClientMaxZoom() != radar.ClientMaxZoom {
		t.Fatal("service did not follow the key off")
	}
}

// A design-key route is always mounted and answers the router's 404 while its key is off; flipping the store opens and closes it with no new router.
func TestDesignKeyGateFollowsLiveKeys(t *testing.T) {
	p, write := liveKeysFile(t, "design_preview=\n")
	ks, _ := livekeys.New(p, "", nil)
	d := Deps{DesignKeys: ks.Keys}
	r := chi.NewRouter()
	for _, k := range []string{parkedGraphKey, tripReceiptKey} {
		r.With(requireDesignKeyMW(d, k)).Get("/"+k, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	}
	code := func(key string) int {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+key, nil))
		return rec.Code
	}
	for _, k := range []string{parkedGraphKey, tripReceiptKey} {
		if c := code(k); c != http.StatusNotFound {
			t.Fatalf("%s off = %d, want 404", k, c)
		}
		write("design_preview=" + k + "\n")
		ks.Reload()
		if c := code(k); c != http.StatusOK {
			t.Fatalf("%s on = %d, want 200", k, c)
		}
		write("design_preview=\n")
		ks.Reload()
		if c := code(k); c != http.StatusNotFound {
			t.Fatalf("%s off again = %d, want 404", k, c)
		}
	}
}

// N pods' worth of readers on one router while the list flips: every /api/config body is one whole list.
func TestConfigNReadersWhileFlipping(t *testing.T) {
	p, write := liveKeysFile(t, "design_preview=a,b\n")
	ks, _ := livekeys.New(p, "", nil)
	h := handleConfig(false, false, false, false, nil, nil, configExtras{DesignKeys: ks.Keys})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, _, _ := configOf(t, h)
				if !reflect.DeepEqual(got, []string{"a", "b"}) && !reflect.DeepEqual(got, []string{"c"}) {
					t.Errorf("torn list %v", got)
					return
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		if i%2 == 0 {
			write("design_preview=c\n")
		} else {
			write("design_preview=a,b\n")
		}
		ks.Reload()
	}
	close(stop)
	wg.Wait()
}
