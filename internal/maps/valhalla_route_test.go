package maps

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// encode6 encodes (lat, lon) pairs as a polyline at precision 6 (the test's
// own encoder, independent of the decoder under test).
func encode6(pts [][2]float64) string {
	var sb strings.Builder
	var pl, pn int
	put := func(v int) {
		v <<= 1
		if v < 0 {
			v = ^v
		}
		for v >= 0x20 {
			sb.WriteByte(byte((0x20 | (v & 0x1f)) + 63))
			v >>= 5
		}
		sb.WriteByte(byte(v + 63))
	}
	for _, p := range pts {
		la, lo := int(p[0]*1e6+0.5*sign(p[0])), int(p[1]*1e6+0.5*sign(p[1]))
		put(la - pl)
		put(lo - pn)
		pl, pn = la, lo
	}
	return sb.String()
}

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

func TestDecodePolylineRoundTripAndTruncation(t *testing.T) {
	pts := [][2]float64{{34.156123, -118.167891}, {34.2, -118.3}, {36.169901, -115.139812}}
	got := decodePolyline(encode6(pts), 1e6)
	if len(got) != len(pts) {
		t.Fatalf("decoded %d points, want %d", len(got), len(pts))
	}
	for i := range pts {
		if d := got[i][0] - pts[i][0]; d > 2e-6 || d < -2e-6 {
			t.Errorf("point %d lat = %v, want %v", i, got[i][0], pts[i][0])
		}
	}
	// Truncated input ends the decode rather than reading past the string.
	enc := encode6(pts)
	for cut := 1; cut < len(enc); cut++ {
		_ = decodePolyline(enc[:cut], 1e6)
	}
	if decodePolyline("", 1e6) != nil {
		t.Error("empty polyline should decode to nothing")
	}
	// polyline5 keeps decoding as before.
	if got := decodePolyline5("_p~iF~ps|U_ulLnnqC_mqNvxq`@"); len(got) != 3 || got[0][0] < 38.49 || got[0][0] > 38.51 {
		t.Errorf("polyline5 = %v", got)
	}
}

func TestValhallaRoute(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = nil
		_ = json.Unmarshal(raw, &body)
		if r.URL.Path != "/route" {
			http.NotFound(w, r)
			return
		}
		a := encode6([][2]float64{{34.0, -118.0}, {34.5, -117.5}})
		b := encode6([][2]float64{{34.5, -117.5}, {35.0, -117.0}})
		_, _ = w.Write([]byte(`{"trip":{"legs":[
		  {"summary":{"length":80.5,"time":3600},"shape":` + jsonStr(a) + `},
		  {"summary":{"length":90.25,"time":4000},"shape":` + jsonStr(b) + `}]}}`))
	}))
	defer srv.Close()
	v := NewValhalla(srv.URL)

	r, err := v.Route(context.Background(), [][2]float64{{34, -118}, {34.5, -117.5}, {35, -117}}, RouteOpts{AvoidTolls: true, AvoidHighways: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Legs) != 2 || r.Legs[0].LengthMeters != 80500 || r.Legs[1].TimeSec != 4000 || len(r.Legs[0].Shape) != 2 {
		t.Errorf("route = %+v", r)
	}
	locs := body["locations"].([]any)
	if len(locs) != 3 || locs[1].(map[string]any)["type"] != "break" {
		t.Errorf("locations sent = %v (every location must be a break so each gap is a leg)", locs)
	}
	if body["shape_format"] != "polyline6" {
		t.Errorf("shape_format = %v", body["shape_format"])
	}
	co := body["costing_options"].(map[string]any)["auto"].(map[string]any)
	if co["use_tolls"] != float64(0) || co["use_highways"] != float64(0) || co["use_ferry"] != nil {
		t.Errorf("costing_options.auto = %v, want tolls and highways avoided only", co)
	}

	// No preferences: the request carries no costing_options at all.
	if _, err := v.Route(context.Background(), [][2]float64{{34, -118}, {34.5, -117.5}, {35, -117}}, RouteOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, has := body["costing_options"]; has {
		t.Error("costing_options must be absent when nothing is avoided")
	}

	// Input and answer checks.
	if _, err := v.Route(context.Background(), [][2]float64{{34, -118}}, RouteOpts{}); err == nil {
		t.Error("one location must be refused")
	}
	if _, err := v.Route(context.Background(), make([][2]float64, 51), RouteOpts{}); err == nil {
		t.Error("51 locations must be refused")
	}
	if _, err := v.Route(context.Background(), [][2]float64{{34, -118}, {35, -117}}, RouteOpts{}); err == nil {
		t.Error("a leg-count mismatch must be an error")
	}
	var nilV *Valhalla
	if _, err := nilV.Route(context.Background(), [][2]float64{{1, 1}, {2, 2}}, RouteOpts{}); err == nil {
		t.Error("an unconfigured client must be an error")
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }
