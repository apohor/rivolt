package maps

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
		{"not json passes", "/route", "nope", 204},
		{"body over 1 MiB", "/trace_route", `{"shape":[],"pad":"` + strings.Repeat("x", MaxValhallaBody) + `"}`, 413},
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
