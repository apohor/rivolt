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
