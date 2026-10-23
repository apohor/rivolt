package geocoding

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PhotonClient wraps a self-hosted Photon HTTP API. Photon's GeoJSON
// FeatureCollection is mapped onto the same Result shape Open-Meteo
// uses so the SPA contract stays unchanged. Empty BaseURL disables
// the client; the multiplexer falls through to Open-Meteo.
type PhotonClient struct {
	HTTP    *http.Client
	BaseURL string
}

// NewPhotonClient returns a Photon client. BaseURL is the in-cluster
// Service URL (e.g. http://photon.photon.svc.cluster.local), no
// trailing slash. Empty disables.
func NewPhotonClient(baseURL string) *PhotonClient {
	return &PhotonClient{
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		BaseURL: strings.TrimRight(baseURL, "/"),
	}
}

// Enabled reports whether a base URL is configured.
func (c *PhotonClient) Enabled() bool {
	return c != nil && c.BaseURL != ""
}

// Search forward-geocodes name through Photon's /api endpoint. Maps
// Photon properties onto the Result shape: name combines housenumber
// + street when present, falls through to .name; admin1 = state;
// country_code = countrycode (Photon emits lowercased, we uppercase
// to match Open-Meteo). Population isn't published by Photon — left
// at zero so the SPA's "highest-population first" sort still works
// for Open-Meteo fallbacks.
func (c *PhotonClient) Search(ctx context.Context, name string, count int) ([]Result, error) {
	if !c.Enabled() {
		return nil, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if count <= 0 {
		count = 5
	}
	if count > 10 {
		count = 10
	}

	// Ask for a few more than shown: duplicates are dropped below.
	q := url.Values{}
	q.Set("q", name)
	q.Set("limit", strconv.Itoa(min(count+3, 10)))
	q.Set("lang", "en")

	u := c.BaseURL + "/api/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("photon: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("photon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("photon: upstream HTTP %d", resp.StatusCode)
	}

	var body struct {
		Features []struct {
			Geometry struct {
				Coordinates [2]float64 `json:"coordinates"` // [lon, lat]
			} `json:"geometry"`
			Properties struct {
				Name        string `json:"name"`
				HouseNumber string `json:"housenumber"`
				Street      string `json:"street"`
				City        string `json:"city"`
				State       string `json:"state"`
				Country     string `json:"country"`
				CountryCode string `json:"countrycode"`
				Postcode    string `json:"postcode"`
				Type        string `json:"type"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("photon: decode: %w", err)
	}

	out := make([]Result, 0, count)
	for _, f := range body.Features {
		if len(out) == count {
			break
		}
		p := f.Properties
		// Build a label that reads naturally regardless of result
		// type: "123 Main St" for a housenumber, "Main St" for a
		// street, "Austin" for a city, the raw name otherwise.
		var label string
		switch {
		case p.HouseNumber != "" && p.Street != "":
			label = p.HouseNumber + " " + p.Street
		case p.Street != "":
			label = p.Street
		case p.Name != "":
			label = p.Name
		case p.City != "":
			label = p.City
		default:
			label = strings.TrimSpace(strings.Join([]string{p.City, p.State, p.Country}, ", "))
		}
		// City row when result is a street/housenumber; helpful
		// disambiguation surface back to the SPA's existing UI.
		if (p.HouseNumber != "" || p.Street != "") && p.City != "" && !strings.Contains(label, p.City) {
			label = label + ", " + p.City
		}
		r := Result{
			Name:        label,
			Latitude:    f.Geometry.Coordinates[1],
			Longitude:   f.Geometry.Coordinates[0],
			Country:     p.Country,
			CountryCode: strings.ToUpper(p.CountryCode),
			Admin1:      p.State,
		}
		if duplicateOf(out, r) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// sameResultM is how close two results with the same label must be to count as one place.
// OSM often holds one street as several ways (and one address as a node and a building), so
// Photon returns "Rose Bowl Drive" twice a few hundred metres apart; a street of that name in
// another town is kilometres away and stays.
const sameResultM = 1000

// duplicateOf reports whether r reads the same as a result already kept (label, state and
// country, ignoring case and spacing) and lies within sameResultM of it.
func duplicateOf(kept []Result, r Result) bool {
	key := resultKey(r)
	for _, k := range kept {
		if resultKey(k) == key && distanceM(k.Latitude, k.Longitude, r.Latitude, r.Longitude) <= sameResultM {
			return true
		}
	}
	return false
}

func resultKey(r Result) string {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	return norm(r.Name) + "|" + norm(r.Admin1) + "|" + norm(r.Country)
}

func distanceM(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * 6371008.8 * math.Asin(math.Min(1, math.Sqrt(a)))
}
