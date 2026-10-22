package maps

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func points(key string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"%s":[`, key)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"lat":32.7767,"lon":-96.797}`)
	}
	b.WriteString(`],"costing":"auto"}`)
	return b.String()
}

func TestLimitValhalla(t *testing.T) {
	t.Parallel()
	var got []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		if r.ContentLength != int64(len(got)) {
			t.Errorf("content length %d, body %d", r.ContentLength, len(got))
		}
		w.WriteHeader(204)
	})
	h := LimitValhalla(next)
	cases := []struct {
		name, path, body string
		want             int
	}{
		{"trace at the cap", "/trace_route", points("shape", MaxTraceShapePoints), 204},
		{"trace over the cap", "/trace_route", points("shape", MaxTraceShapePoints+1), 413},
		{"route at the cap", "/route", points("locations", MaxRouteLocations), 204},
		{"route over the cap", "/route", points("locations", MaxRouteLocations+1), 413},
		{"shape on route is not counted", "/route", points("shape", MaxTraceShapePoints+1), 204},
		{"the web's trace body", "/trace_route", `{"shape":[{"lat":1,"lon":2,"time":3}],"costing":"auto","shape_match":"walk_or_snap","trace_options":{"search_radius":100,"gps_accuracy":25},"format":"osrm"}`, 204},
		{"the web's route body", "/route", `{"locations":[{"lat":1,"lon":2,"type":"break"},{"lat":3,"lon":4,"type":"through"}],"costing":"auto","directions_options":{"units":"miles"}}`, 204},
		{"body over 1 MiB", "/trace_route", `{"shape":[],"pad":"` + strings.Repeat("x", MaxValhallaBody) + `"}`, 413},
		// What the counts can't see is refused.
		{"not json", "/route", "nope", 400},
		{"empty body", "/route", "", 400},
		{"a list, not an object", "/route", "[]", 400},
		{"trailing data", "/route", `{"locations":[]}{"locations":[]}`, 400},
		{"unterminated", "/route", `{"locations":[]`, 400},
		{"encoded polyline", "/trace_route", `{"encoded_polyline":"_p~iF~ps|U_ulLnnqC","costing":"auto"}`, 400},
		{"encoded polyline in another case", "/trace_route", `{"Encoded_Polyline":"_p~iF~ps|U","costing":"auto"}`, 400},
		// Every copy is counted, so a long one can't hide behind a short one Go's decoder would keep.
		{"shape repeated, the long copy first", "/trace_route", `{"shape":` + strings.TrimSuffix(strings.TrimPrefix(points("shape", MaxTraceShapePoints+1), `{"shape":`), `,"costing":"auto"}`) + `,"shape":[]}`, 413},
		{"shape repeated", "/trace_route", `{"shape":[],"shape":[]}`, 400},
		{"shape in another case", "/trace_route", `{"Shape":[]}`, 400},
		{"escaped key repeats", "/route", `{"locations":[],"loc\u0061tions":[]}`, 400},
		{"shape not a list", "/trace_route", `{"shape":"x"}`, 400},
	}
	for _, c := range cases {
		got = nil
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "http://x"+c.path, strings.NewReader(c.body)))
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
			continue
		}
		// What passes reaches Valhalla byte for byte.
		if c.want == 204 && !bytes.Equal(got, []byte(c.body)) {
			t.Errorf("%s: body changed on the way through", c.name)
		}
	}
}

// Valhalla reads a whole request from ?json=, which would skip the body checks: no query string reaches it.
func TestLimitValhallaRefusesAQueryString(t *testing.T) {
	t.Parallel()
	reached := false
	h := LimitValhalla(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	for _, target := range []string{
		"http://x/trace_route?json=" + url.QueryEscape(points("shape", MaxTraceShapePoints+1)),
		"http://x/route?json=%7B%7D",
		"http://x/route?",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", target, strings.NewReader(`{"locations":[]}`)))
		if rec.Code != http.StatusBadRequest || reached {
			t.Errorf("%s: %d (reached %v), want 400 and not reached", target, rec.Code, reached)
		}
	}
}
