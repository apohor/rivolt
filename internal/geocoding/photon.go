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
	return c.SearchNear(ctx, name, count, nil)
}

// Near is a point the search ranks nearer places higher around (a map centre or the truck).
type Near struct{ Lat, Lon float64 }

// Photon's location bias: a weak pull (scale) that reaches far (zoom 9 is a couple of hundred kilometres), so the nearest match of a
// name outranks the same name far away, whether a hotel chain or a street. It also pushes a faraway famous city below nearby
// namesakes, so with a point SearchNear also asks without it and keeps the top unbiasedKept of those in the list.
const (
	biasZoom      = "9"
	biasScale     = "0.1"
	unbiasedKept  = 2
	biasedFirst   = 3
	maxPhotonRows = 10
)

// SearchNear is Search with an optional point to rank around. With one it makes two Photon requests at once, one biased and one
// plain, and shows the biased list with the plain list's best unbiasedKept places worked in, so a far exact match ("Boston" from
// Texas) still comes back. A failed plain request is ignored: the biased list stands alone.
func (c *PhotonClient) SearchNear(ctx context.Context, name string, count int, near *Near) ([]Result, error) {
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
	if near == nil {
		return c.searchRows(ctx, name, count, nil)
	}
	type answer struct {
		res []Result
		err error
	}
	plainCh := make(chan answer, 1)
	go func() {
		r, err := c.searchRows(ctx, name, count, nil)
		plainCh <- answer{r, err}
	}()
	biased, err := c.searchRows(ctx, name, count, near)
	plain := <-plainCh
	if err != nil {
		return nil, err
	}
	if plain.err != nil || len(plain.res) == 0 {
		return biased, nil
	}
	return cityFirst(mergeBiased(biased, plain.res, count), plain.res), nil
}

// isTypedSettlement reports a city or town (place=city or place=town) whose name is the whole query, "Boston" for "boston".
func isTypedSettlement(p photonProps, query string) bool {
	return p.OSMKey == "place" && (p.OSMValue == "city" || p.OSMValue == "town") && p.Name != "" && strings.EqualFold(strings.TrimSpace(p.Name), strings.TrimSpace(query))
}

// cityFirst keeps the bias order but makes sure the most important city or town typed in full (the plain list's first such row; Photon
// ranks by importance, so "boston" is Boston, Massachusetts) is among the first three. Already there: it stays and is marked. Otherwise it
// goes second, or third when a nearby namesake (also a city or town of that name) is already first, so a trip planner near Springfield, MO
// keeps that one on top and the famous far one is still in view. No such place in the plain list: the list is as the merge made it.
func cityFirst(out, plain []Result) []Result {
	var top *Result
	for i := range plain {
		if plain[i].settlement {
			top = &plain[i]
			break
		}
	}
	if top == nil || len(out) == 0 {
		return out
	}
	for i := 0; i < len(out) && i < biasedFirst; i++ {
		if duplicateOf([]Result{*top}, out[i]) {
			out[i].Exact = true
			return out
		}
	}
	at := 1
	if out[0].settlement {
		at = 2
	}
	at = min(at, len(out)-1)
	first := *top
	first.Exact = true
	res := make([]Result, 0, len(out))
	for _, r := range out {
		if !duplicateOf([]Result{first}, r) {
			res = append(res, r)
		}
	}
	res = append(res[:min(at, len(res))], append([]Result{first}, res[min(at, len(res)):]...)...)
	return res[:min(len(res), len(out))]
}

// mergeBiased keeps the first biasedFirst biased results, then the plain list's best places not already there, then the
// rest of the biased list, up to count.
func mergeBiased(biased, plain []Result, count int) []Result {
	keep := max(min(biasedFirst, count-unbiasedKept), 0)
	out := make([]Result, 0, count)
	for _, r := range biased {
		if len(out) == keep {
			break
		}
		out = append(out, r)
	}
	added := 0
	for _, r := range plain {
		if added == unbiasedKept || len(out) == count {
			break
		}
		if !duplicateOf(out, r) {
			out = append(out, r)
			added++
		}
	}
	for _, r := range biased {
		if len(out) == count {
			break
		}
		if !duplicateOf(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func (c *PhotonClient) searchRows(ctx context.Context, name string, count int, near *Near) ([]Result, error) {
	// Ask for a few more than shown: duplicates are dropped below.
	q := url.Values{}
	q.Set("q", name)
	q.Set("limit", strconv.Itoa(min(count+3, maxPhotonRows)))
	q.Set("lang", "en")
	if near != nil {
		q.Set("lat", strconv.FormatFloat(near.Lat, 'f', 4, 64))
		q.Set("lon", strconv.FormatFloat(near.Lon, 'f', 4, 64))
		q.Set("zoom", biasZoom)
		q.Set("location_bias_scale", biasScale)
	}

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
			Properties photonProps `json:"properties"`
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
		r := searchResultFromProps(f.Properties, f.Geometry.Coordinates[1], f.Geometry.Coordinates[0])
		r.settlement = isTypedSettlement(f.Properties, name)
		if duplicateOf(out, r) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// photonProps are the Photon feature properties both Search and Reverse read.
type photonProps struct {
	Name        string `json:"name"`
	HouseNumber string `json:"housenumber"`
	Street      string `json:"street"`
	City        string `json:"city"`
	State       string `json:"state"`
	Country     string `json:"country"`
	CountryCode string `json:"countrycode"`
	Postcode    string `json:"postcode"`
	Type        string `json:"type"`
	OSMKey      string `json:"osm_key"`
	OSMValue    string `json:"osm_value"`
}

// resultFromProps maps one Photon feature onto a Result. The label reads naturally whatever the result type: "123 Main St" for a
// housenumber, "Main St" for a street, "Austin" for a city, the raw name otherwise, and a street result gains its city.
func resultFromProps(p photonProps, lat, lon float64) Result {
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
	if (p.HouseNumber != "" || p.Street != "") && p.City != "" && !strings.Contains(label, p.City) {
		label = label + ", " + p.City
	}
	var locality string
	if p.HouseNumber != "" || p.Street != "" {
		locality = p.City
	}
	return Result{
		Name:        label,
		Locality:    locality,
		Latitude:    lat,
		Longitude:   lon,
		Country:     p.Country,
		CountryCode: strings.ToUpper(p.CountryCode),
		Admin1:      p.State,
	}
}

// isPOI reports a named place that is neither an address, a street nor a settlement or boundary: a hotel, a charger, a park.
// Photon gives such a place its street and house number too, which would hide its name behind an address.
func isPOI(p photonProps) bool {
	if p.Name == "" || p.OSMKey == "" || p.OSMKey == "place" || p.OSMKey == "boundary" {
		return false
	}
	return !(p.OSMKey == "highway" && p.Type == "street")
}

// searchResultFromProps is resultFromProps for a text search: a POI keeps its own name and gains its town ("Hampton Inn & Suites,
// Dumas"; no repeat when the name already holds the town), and the town is its Locality. Reverse lookups keep resultFromProps
// (an address, never a business name).
func searchResultFromProps(p photonProps, lat, lon float64) Result {
	if !isPOI(p) {
		return resultFromProps(p, lat, lon)
	}
	label := p.Name
	if p.City != "" && !strings.Contains(strings.ToLower(label), strings.ToLower(p.City)) {
		label += ", " + p.City
	}
	return Result{
		Name:        label,
		Locality:    p.City,
		Latitude:    lat,
		Longitude:   lon,
		Country:     p.Country,
		CountryCode: strings.ToUpper(p.CountryCode),
		Admin1:      p.State,
	}
}

// Reverse names the place at a point through Photon's /reverse endpoint: the nearest feature, mapped like a Search result. A point with
// nothing near it is (nil, nil); a disabled client or an upstream failure is an error the caller treats as "no label".
func (c *PhotonClient) Reverse(ctx context.Context, lat, lon float64) (*Result, error) {
	if !c.Enabled() {
		return nil, nil
	}
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(lat, 'f', 5, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', 5, 64))
	q.Set("limit", "1")
	q.Set("lang", "en")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/reverse?"+q.Encode(), nil)
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
			Properties photonProps `json:"properties"`
		} `json:"features"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("photon: decode: %w", err)
	}
	if len(body.Features) == 0 {
		return nil, nil
	}
	f := body.Features[0]
	r := resultFromProps(f.Properties, f.Geometry.Coordinates[1], f.Geometry.Coordinates[0])
	return &r, nil
}

// ReverseArea names the area around a point at city or town level, for a place the caller must not show more exactly: the city or town when
// the nearest one is a city or a town (a village or hamlet is too small to hide anyone), else the county, else the state. The result is
// the name plus " area" ("Pasadena area"); nothing finer than that is ever returned (no street, house, POI or postcode). "" with no error
// means no place was found; a disabled client or an upstream failure is an error.
func (c *PhotonClient) ReverseArea(ctx context.Context, lat, lon float64) (string, error) {
	if !c.Enabled() {
		return "", nil
	}
	for _, l := range []struct {
		layer  string
		radius string
		ok     func(photonProps) bool
	}{
		{"city", "25", func(p photonProps) bool { v := strings.ToLower(p.OSMValue); return v == "city" || v == "town" }},
		{"county", "100", func(photonProps) bool { return true }},
		{"state", "300", func(photonProps) bool { return true }},
	} {
		q := url.Values{}
		q.Set("lat", strconv.FormatFloat(lat, 'f', 5, 64))
		q.Set("lon", strconv.FormatFloat(lon, 'f', 5, 64))
		q.Set("limit", "5")
		q.Set("lang", "en")
		q.Set("layer", l.layer)
		q.Set("radius", l.radius)
		props, err := c.reverseProps(ctx, q)
		if err != nil {
			return "", err
		}
		for _, p := range props {
			if p.Name != "" && l.ok(p) {
				return p.Name + " area", nil
			}
		}
	}
	return "", nil
}

func (c *PhotonClient) reverseProps(ctx context.Context, q url.Values) ([]photonProps, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/reverse?"+q.Encode(), nil)
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
			Properties photonProps `json:"properties"`
		} `json:"features"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("photon: decode: %w", err)
	}
	out := make([]photonProps, 0, len(body.Features))
	for _, f := range body.Features {
		out = append(out, f.Properties)
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

// Lodging is a hotel or motel from the OpenStreetMap data Photon serves.
type Lodging struct {
	Name string
	Lat  float64
	Lon  float64
	// ID is the OpenStreetMap element, such as "osm:node:123456".
	ID string
	// Locality is the town, when Photon has one.
	Locality string
}

// Lodging lists hotels and motels within radiusKm of a point through Photon's /reverse endpoint with an osm_tag filter (the text
// search only biases by location, so it returns hotels nationwide). Nameless places are dropped. Best effort for the caller: an
// error means "none found". limit is capped at 20 and radiusKm at 50.
func (c *PhotonClient) Lodging(ctx context.Context, lat, lon, radiusKm float64, limit int) ([]Lodging, error) {
	if !c.Enabled() {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	if radiusKm <= 0 || radiusKm > 50 {
		radiusKm = 50
	}
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(lat, 'f', 5, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', 5, 64))
	q.Set("radius", strconv.FormatFloat(radiusKm, 'f', 1, 64))
	q.Set("limit", strconv.Itoa(limit))
	q.Set("lang", "en")
	q.Add("osm_tag", "tourism:hotel")
	q.Add("osm_tag", "tourism:motel")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/reverse?"+q.Encode(), nil)
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
				Name     string `json:"name"`
				City     string `json:"city"`
				OSMID    int64  `json:"osm_id"`
				OSMType  string `json:"osm_type"` // "N", "W" or "R"
				District string `json:"district"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("photon: decode: %w", err)
	}
	var out []Lodging
	seen := map[string]bool{}
	for _, f := range body.Features {
		p := f.Properties
		name := strings.TrimSpace(p.Name)
		lat, lon := f.Geometry.Coordinates[1], f.Geometry.Coordinates[0]
		if name == "" || math.IsNaN(lat) || math.IsNaN(lon) || (lat == 0 && lon == 0) {
			continue
		}
		kind := map[string]string{"N": "node", "W": "way", "R": "relation"}[strings.ToUpper(p.OSMType)]
		id := ""
		if kind != "" && p.OSMID != 0 {
			id = "osm:" + kind + ":" + strconv.FormatInt(p.OSMID, 10)
			if seen[id] {
				continue
			}
			seen[id] = true
		}
		loc := p.City
		if loc == "" {
			loc = p.District
		}
		out = append(out, Lodging{Name: name, Lat: lat, Lon: lon, ID: id, Locality: loc})
	}
	return out, nil
}
