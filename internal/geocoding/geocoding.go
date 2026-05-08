// Package geocoding wraps Open-Meteo's free geocoding endpoint
// (https://geocoding-api.open-meteo.com/v1/search) for forward-
// geocoding text input → lat/lon. Same provider we use for weather,
// so the privacy trade-off is consistent: city/place names leave the
// box during search, but no per-user identifiers are sent.
//
// The endpoint resolves city- and town-level inputs (Dallas, Big Bend
// National Park). It does NOT do street addresses; for those, a
// future slice would need a self-hosted Nominatim (heavyweight) or a
// keyed commercial geocoder. The trip-planner use case in slice 2 is
// "where am I going at the city level," which this covers.
package geocoding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is Open-Meteo's geocoding endpoint. Override on the
// Client for tests.
const DefaultBaseURL = "https://geocoding-api.open-meteo.com"

// Result is one search match. Fields mirror Open-Meteo's response,
// renamed to Go conventions; the SPA gets these names verbatim via
// the API handler.
type Result struct {
	Name        string  `json:"name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"country_code,omitempty"`
	// Admin1 is state / province / region. Useful for disambiguating
	// "Dallas, TX" from "Dallas, OR".
	Admin1 string `json:"admin1,omitempty"`
	// Population helps the SPA show the relevance order Open-Meteo
	// already applies (top result is highest-population match).
	Population int `json:"population,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
}

// Client is a thin wrapper around the geocoding endpoint.
type Client struct {
	HTTP    *http.Client
	BaseURL string
}

// NewClient returns a Client with sane defaults.
func NewClient() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		BaseURL: DefaultBaseURL,
	}
}

// Search forward-geocodes name, returning at most count matches
// (capped to 10 so a typo can't pull a long page). Returns an empty
// slice (not an error) when Open-Meteo finds nothing.
func (c *Client) Search(ctx context.Context, name string, count int) ([]Result, error) {
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

	q := url.Values{}
	q.Set("name", name)
	q.Set("count", strconv.Itoa(count))
	q.Set("language", "en")
	q.Set("format", "json")

	u := c.BaseURL + "/v1/search?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("geocoding: build request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("geocoding: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geocoding: upstream HTTP %d", resp.StatusCode)
	}

	var body struct {
		Results []Result `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("geocoding: decode: %w", err)
	}
	return body.Results, nil
}
