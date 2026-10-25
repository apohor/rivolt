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
