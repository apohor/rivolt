package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Against a model chain the server answers one precipitation series per model; the sum over the next hours takes each hour from the first
// model that has it, and only the hours inside the window count.
func TestFetchRainAheadSumsTheWindowFromTheChain(t *testing.T) {
	h0 := time.Now().UTC().Truncate(time.Hour)
	f := func(d int) string { return `"` + h0.Add(time.Duration(d)*time.Hour).Format("2006-01-02T15:04") + `"` }
	var vars, models string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars, models = r.URL.Query().Get("hourly"), r.URL.Query().Get("models")
		_, _ = w.Write([]byte(`{"hourly":{"time":[` + f(-1) + `,` + f(0) + `,` + f(1) + `,` + f(2) + `,` + f(3) + `],` +
			`"precipitation_ncep_hrrr_conus":[7,0.4,null,null,8],"precipitation_ncep_gfs013":[7,0.9,0.5,null,8],"precipitation_gem_seamless":[7,0.9,0.9,0.2,8]}}`))
	}))
	defer srv.Close()
	mm, ok, err := NewSelfHostedClient(srv.URL).FetchRainAhead(context.Background(), 34.2, -118.2, time.Now(), 3)
	if err != nil || !ok || mm != 1.1 { // 0.4 (hrrr) + 0.5 (gfs) + 0.2 (gem)
		t.Fatalf("mm = %v, ok %v, err %v; want 1.1", mm, ok, err)
	}
	if vars != "precipitation" || models != selfHostedChain {
		t.Errorf("asked for %q with models %q", vars, models)
	}
}

// No model answered any hour: unknown, not dry.
func TestFetchRainAheadAllNullIsUnknown(t *testing.T) {
	h0 := time.Now().UTC().Truncate(time.Hour).Format("2006-01-02T15:04")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"hourly":{"time":["` + h0 + `"],"precipitation_ncep_hrrr_conus":[null],"precipitation_ncep_gfs013":[null],"precipitation_gem_seamless":[null]}}`))
	}))
	defer srv.Close()
	mm, ok, err := NewSelfHostedClient(srv.URL).FetchRainAhead(context.Background(), 34.2, -118.2, time.Now(), 3)
	if err != nil || ok || mm != 0 {
		t.Fatalf("mm = %v, ok %v, err %v; want unknown", mm, ok, err)
	}
}
