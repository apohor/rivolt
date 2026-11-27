package geocoding

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Photon returns one street as several OSM ways, and one address as a node and a
// building: the same label, a few hundred metres apart. Each shows once; the same
// name in another town stays. Synthetic positions.
func TestPhotonSearchDropsDuplicates(t *testing.T) {
	feature := func(lat, lon float64, props string) string {
		return fmt.Sprintf(`{"geometry":{"coordinates":[%v,%v]},"properties":{%s}}`, lon, lat, props)
	}
	street := `"street":"Rose Bowl Drive","city":"Pasadena","state":"California","country":"United States","countrycode":"us"`
	house := `"housenumber":"1001",` + street
	var gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		fs := []string{
			feature(34.1613, -118.1676, house),
			feature(34.1615, -118.1679, house), // the building of the same address
			feature(34.1580, -118.1700, street),
			feature(34.1620, -118.1650, strings.Replace(street, "Rose Bowl", "ROSE  BOWL", 1)), // another way of it
			feature(34.0500, -117.9000, strings.Replace(street, "Pasadena", "Elsewhere", 1)),
			feature(36.0000, -115.0000, `"street":"Rose Bowl Drive","city":"Pasadena","state":"Nevada","country":"United States"`),
		}
		_, _ = w.Write([]byte(`{"features":[` + strings.Join(fs, ",") + `]}`))
	}))
	defer srv.Close()

	got, err := NewPhotonClient(srv.URL).Search(context.Background(), "rose bowl", 5)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name+" / "+r.Admin1)
	}
	want := []string{
		"1001 Rose Bowl Drive, Pasadena / California",
		"Rose Bowl Drive, Pasadena / California",
		"Rose Bowl Drive, Elsewhere / California",
		"Rose Bowl Drive, Pasadena / Nevada",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("results:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
	if gotLimit != "8" {
		t.Errorf("asked Photon for %s, want 8 (5 shown plus room for duplicates)", gotLimit)
	}

	got, _ = NewPhotonClient(srv.URL).Search(context.Background(), "rose bowl", 2)
	if len(got) != 2 {
		t.Errorf("count 2 gave %d results", len(got))
	}
}

// Lodging asks /reverse (not the text search) for hotels and motels within a radius, drops the nameless, and names each place by
// its OpenStreetMap element. Synthetic positions.
func TestPhotonLodgingAsksReverseWithTheTagFilter(t *testing.T) {
	var path string
	var q map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, q = r.URL.Path, r.URL.Query()
		_, _ = w.Write([]byte(`{"features":[
		 {"geometry":{"coordinates":[-104.44,36.90]},"properties":{"name":"Example Motor Inn","city":"Exampleton","osm_id":111,"osm_type":"N"}},
		 {"geometry":{"coordinates":[-104.45,36.91]},"properties":{"name":"","osm_id":112,"osm_type":"N"}},
		 {"geometry":{"coordinates":[-104.46,36.92]},"properties":{"name":"Sample Lodge","district":"Dist","osm_id":113,"osm_type":"W"}},
		 {"geometry":{"coordinates":[-104.46,36.92]},"properties":{"name":"Sample Lodge","osm_id":113,"osm_type":"W"}},
		 {"geometry":{"coordinates":[0,0]},"properties":{"name":"At null island","osm_id":114,"osm_type":"N"}}]}`))
	}))
	defer srv.Close()
	got, err := NewPhotonClient(srv.URL).Lodging(context.Background(), 36.9, -104.44, 25, 10)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/reverse" || q["radius"][0] != "25.0" || q["limit"][0] != "10" || len(q["osm_tag"]) != 2 || q["osm_tag"][0] != "tourism:hotel" || q["osm_tag"][1] != "tourism:motel" {
		t.Errorf("request %s %v", path, q)
	}
	if len(got) != 2 || got[0].Name != "Example Motor Inn" || got[0].ID != "osm:node:111" || got[0].Locality != "Exampleton" || got[1].ID != "osm:way:113" || got[1].Locality != "Dist" {
		t.Errorf("got %+v", got)
	}
	if got[0].Lat != 36.90 || got[0].Lon != -104.44 {
		t.Errorf("coordinates %v %v, want lat 36.90 lon -104.44", got[0].Lat, got[0].Lon)
	}
}

func TestPhotonLodgingIsBestEffort(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer down.Close()
	if got, err := NewPhotonClient(down.URL).Lodging(context.Background(), 1, 1, 10, 5); err == nil || got != nil {
		t.Errorf("a 502 must be an error with no places: %v %v", got, err)
	}
	if got, err := NewPhotonClient("").Lodging(context.Background(), 1, 1, 10, 5); err != nil || got != nil {
		t.Errorf("disabled: %v %v", got, err)
	}
}

// The area of a point is a city or a town; a village falls to the county, nothing near falls to the state; never a street or a POI.
func TestPhotonReverseAreaIsCityOrCountyOnly(t *testing.T) {
	var layers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		layer := r.URL.Query().Get("layer")
		layers = append(layers, layer)
		switch layer {
		case "city":
			switch r.URL.Query().Get("lat") {
			case "34.15000": // a city is near
				_, _ = w.Write([]byte(`{"features":[{"properties":{"name":"Hamlet","osm_value":"hamlet"}},{"properties":{"name":"Pasadena","osm_value":"city","street":"Rose Bowl Drive"}}]}`))
			case "36.90000": // only a village is near
				_, _ = w.Write([]byte(`{"features":[{"properties":{"name":"Tiny Village","osm_value":"village"}}]}`))
			default:
				_, _ = w.Write([]byte(`{"features":[]}`))
			}
		case "county":
			if r.URL.Query().Get("lat") == "0.00000" {
				_, _ = w.Write([]byte(`{"features":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"features":[{"properties":{"name":"Example County","osm_value":"administrative"}}]}`))
		default:
			_, _ = w.Write([]byte(`{"features":[{"properties":{"name":"Example State"}}]}`))
		}
	}))
	defer srv.Close()
	c := NewPhotonClient(srv.URL)
	for _, tc := range []struct {
		lat  float64
		want string
	}{{34.15, "Pasadena area"}, {36.9, "Example County area"}, {0, "Example State area"}} {
		got, err := c.ReverseArea(context.Background(), tc.lat, -118.15)
		if err != nil || got != tc.want {
			t.Errorf("ReverseArea(%v) = %q, %v; want %q", tc.lat, got, err, tc.want)
		}
	}
	for _, l := range layers {
		if l != "city" && l != "county" && l != "state" {
			t.Errorf("asked layer %q: only city, county and state may be asked", l)
		}
	}
}

// A search with a point asks Photon for a weak, wide location bias and, in a second plain request, for the unbiased order, then
// shows the biased list with the plain list's best two worked in after the first three biased: a far exact name still comes back. Synthetic data.
func TestPhotonSearchNearBiasesAndKeepsFarExactMatch(t *testing.T) {
	feat := func(name, city, state string, lat, lon float64) string {
		return fmt.Sprintf(`{"geometry":{"coordinates":[%v,%v]},"properties":{"name":%q,"city":%q,"state":%q,"osm_key":"tourism","osm_value":"hotel","type":"house","street":"Main St"}}`, lon, lat, name, city, state)
	}
	var biased, plain int
	var got map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("lat") == "" {
			plain++
			_, _ = w.Write([]byte(`{"features":[` + feat("Hampton Inn", "Farville", "Utah", 37, -113) + `,` + feat("Hampton Inn", "Elsewhere", "Ohio", 40, -83) + `]}`))
			return
		}
		biased++
		got = q
		_, _ = w.Write([]byte(`{"features":[` +
			feat("Hampton Inn & Suites Dumas", "Dumas", "Texas", 35.8, -101.9) + `,` +
			feat("Hampton Inn & Suites", "Borger", "Texas", 35.6, -101.4) + `,` +
			feat("Hampton Inn & Suites", "Amarillo", "Texas", 35.2, -101.8) + `,` +
			feat("Hampton Inn & Suites", "Guymon", "Oklahoma", 36.7, -101.5) + `]}`))
	}))
	defer srv.Close()

	res, err := NewPhotonClient(srv.URL).SearchNear(context.Background(), "hampton inn", 4, &Near{Lat: 36.06, Lon: -102.51})
	if err != nil {
		t.Fatal(err)
	}
	if biased != 1 || plain != 1 {
		t.Errorf("requests biased %d plain %d, want 1 and 1", biased, plain)
	}
	for k, want := range map[string]string{"lat": "36.0600", "lon": "-102.5100", "zoom": "9", "location_bias_scale": "0.1"} {
		if got[k][0] != want {
			t.Errorf("%s = %v, want %s", k, got[k], want)
		}
	}
	var names []string
	for _, r := range res {
		names = append(names, r.Name)
	}
	want := "Hampton Inn & Suites Dumas|Hampton Inn & Suites, Borger|Hampton Inn, Farville|Hampton Inn, Elsewhere"
	if strings.Join(names, "|") != want {
		t.Errorf("results %q, want %q", strings.Join(names, "|"), want)
	}

	// Without a point: one plain request, no bias parameters.
	biased, plain = 0, 0
	if _, err := NewPhotonClient(srv.URL).Search(context.Background(), "hampton inn", 4); err != nil {
		t.Fatal(err)
	}
	if biased != 0 || plain != 1 {
		t.Errorf("no point: biased %d plain %d, want 0 and 1", biased, plain)
	}
}

// A failed plain request leaves the biased list alone; a failed biased request is the search's error.
func TestPhotonSearchNearFailures(t *testing.T) {
	ok := `{"features":[{"geometry":{"coordinates":[-101.9,35.8]},"properties":{"name":"Dumas","osm_key":"place","osm_value":"city"}}]}`
	for _, c := range []struct {
		name       string
		failBiased bool
		wantErr    bool
	}{{"plain fails", false, false}, {"biased fails", true, true}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isBiased := r.URL.Query().Get("lat") != ""
			if isBiased == c.failBiased {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(ok))
		}))
		res, err := NewPhotonClient(srv.URL).SearchNear(context.Background(), "dumas", 3, &Near{Lat: 36, Lon: -102})
		srv.Close()
		if (err != nil) != c.wantErr || (!c.wantErr && len(res) != 1) {
			t.Errorf("%s: res %v err %v", c.name, res, err)
		}
	}
}

// "boston" typed near Pasadena: the biased list is hotels and a small Boston, the plain list has the city. The city typed in full comes first
// and is marked, the list keeps its length, and a search with no such city is left as the merge made it.
func TestPhotonSearchNearPutsTheCityTypedInFullFirst(t *testing.T) {
	row := func(name, key, value, city, state string, lat, lon float64) string {
		return fmt.Sprintf(`{"geometry":{"coordinates":[%v,%v]},"properties":{"name":%q,"city":%q,"state":%q,"osm_key":%q,"osm_value":%q}}`, lon, lat, name, city, state, key, value)
	}
	hotel := func(n string, lat float64) string {
		return row(n, "tourism", "hotel", "Pasadena", "California", lat, -118.1)
	}
	var plainRows, biasedRows string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("lat") == "" {
			_, _ = w.Write([]byte(`{"features":[` + plainRows + `]}`))
			return
		}
		_, _ = w.Write([]byte(`{"features":[` + biasedRows + `]}`))
	}))
	defer srv.Close()
	near := &Near{Lat: 34.15, Lon: -118.14}
	names := func(rs []Result) string {
		var n []string
		for _, r := range rs {
			n = append(n, r.Name)
		}
		return strings.Join(n, "|")
	}

	biasedRows = hotel("Boston Inn", 34.1) + "," + hotel("Boston Suites", 34.2) + "," + row("Boston", "place", "village", "", "California", 36, -119) + "," + hotel("Boston Lodge", 34.3) + "," + row("Bostonia", "place", "suburb", "", "California", 32.8, -116.9)
	plainRows = row("Boston", "place", "city", "", "Massachusetts", 42.36, -71.06) + "," + row("Boston", "place", "town", "", "Lincolnshire", 52.97, -0.02)
	res, err := NewPhotonClient(srv.URL).SearchNear(context.Background(), "boston", 5, near)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 5 || res[0].Admin1 != "Massachusetts" || !res[0].Exact {
		t.Fatalf("results %q, want Boston, Massachusetts first and marked, 5 rows", names(res))
	}
	for _, r := range res[1:] {
		if r.Exact || (r.Admin1 == "Massachusetts" && r.Name == "Boston") {
			t.Errorf("%q: one exact city only, no repeat", r.Name)
		}
	}

	// No city or town of that exact name in the plain list (a village, a hotel): nothing moves.
	plainRows = row("Boston", "place", "village", "", "Georgia", 30.8, -83.8)
	res, _ = NewPhotonClient(srv.URL).SearchNear(context.Background(), "boston", 5, near)
	if !strings.HasPrefix(res[0].Name, "Boston Inn") || res[0].Exact {
		t.Errorf("no exact city: results %q, want the merge as it was", names(res))
	}
}
