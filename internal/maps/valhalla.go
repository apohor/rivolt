package maps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Valhalla is a thin server-to-server client for the in-cluster
// Valhalla routing service. It exists alongside the same-origin
// reverse proxy (NewProxy in this package) — the proxy is for the SPA,
// this client is for backend code that wants a routing answer
// directly without going through the user-facing HTTP path.
//
// Today the only consumer is the live recorder's GPS-gap fill: when
// the WS feed drops fixes for several seconds, we ask Valhalla what
// road-level path is most likely between the last good fix and the
// new one, and splice that shape into the drive polyline so the
// rendered trace doesn't contain a straight-line shortcut across
// whatever the car actually drove.
type Valhalla struct {
	HTTP    *http.Client
	BaseURL string
}

// NewValhalla returns a client pointed at baseURL. Empty baseURL
// returns nil — callers must nil-check and skip the feature.
func NewValhalla(baseURL string) *Valhalla {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return nil
	}
	return &Valhalla{
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		BaseURL: baseURL,
	}
}

// Enabled reports whether the client is configured.
func (v *Valhalla) Enabled() bool { return v != nil && v.BaseURL != "" }

// RouteShape returns the on-road shape between two points as a
// sequence of (lat, lon) pairs, derived from a Valhalla /route call
// with auto costing. Used by the recorder to fill GPS-lag gaps with
// a plausible road-snapped path. The returned slice INCLUDES the
// requested endpoints (Valhalla always echoes them as the first/last
// shape vertex).
//
// The context's deadline bounds the upstream call. Callers should
// pass a tight timeout (a few hundred ms) — if Valhalla can't answer
// quickly it's better to fall back to a straight line than to delay
// the live recorder.
func (v *Valhalla) RouteShape(ctx context.Context, from, to [2]float64) ([][2]float64, error) {
	if !v.Enabled() {
		return nil, errors.New("valhalla: not configured")
	}

	body := map[string]any{
		"locations": []map[string]any{
			{"lat": from[0], "lon": from[1], "type": "break"},
			{"lat": to[0], "lon": to[1], "type": "break"},
		},
		"costing": "auto",
		// polyline5 keeps wire format aligned with our existing
		// drive-polyline encoder/decoder precision.
		"shape_format": "polyline5",
		// Skip the verbal/maneuver narrative payload — we only need
		// the geometry.
		"directions_options": map[string]any{"language": "en-US"},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("valhalla: marshal: %w", err)
	}

	u, err := url.Parse(v.BaseURL + "/route")
	if err != nil {
		return nil, fmt.Errorf("valhalla: parse url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("valhalla: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("valhalla: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("valhalla: HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Trip struct {
			Legs []struct {
				Shape string `json:"shape"`
			} `json:"legs"`
		} `json:"trip"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("valhalla: decode: %w", err)
	}

	var out [][2]float64
	for _, leg := range parsed.Trip.Legs {
		out = append(out, decodePolyline5(leg.Shape)...)
	}
	if len(out) == 0 {
		return nil, errors.New("valhalla: empty shape")
	}
	return out, nil
}

// decodePolyline5 decodes a Google-format encoded polyline at
// precision 5 (1e-5 degrees). Mirrors the encoder in
// internal/rivian/polyline.go so an encode/decode round-trip is
// lossless modulo banker's rounding.
func decodePolyline5(s string) [][2]float64 { return decodePolyline(s, 1e5) }

// decodePolyline decodes a Google-format encoded polyline whose
// coordinates are scaled by scale (1e5 for precision 5, 1e6 for 6). A
// truncated string ends the decode instead of reading past it.
func decodePolyline(s string, scale float64) [][2]float64 {
	var (
		out      [][2]float64
		i        int
		lat, lon int
	)
	next := func() (int, bool) {
		var result int32
		var shift uint
		for {
			if i >= len(s) {
				return 0, false
			}
			b := int32(s[i]) - 63
			i++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			return int(^(result >> 1)), true
		}
		return int(result >> 1), true
	}
	for i < len(s) {
		dlat, ok := next()
		if !ok {
			break
		}
		dlon, ok := next()
		if !ok {
			break
		}
		lat += dlat
		lon += dlon
		out = append(out, [2]float64{float64(lat) / scale, float64(lon) / scale})
	}
	return out
}

// RouteOpts are the routing preferences Route honours.
type RouteOpts struct {
	AvoidTolls    bool
	AvoidFerries  bool
	AvoidHighways bool
}

// RouteLeg is one leg of a Route: from one location to the next.
type RouteLeg struct {
	LengthMeters float64
	TimeSec      float64
	// Shape is the leg's road geometry as (lat, lon) pairs.
	Shape [][2]float64
}

// Route is a Valhalla route through an ordered list of locations.
type Route struct {
	Legs []RouteLeg
}

// ErrNoRoute means Valhalla found no road route between the locations (a
// location in the ocean, or islands with no ferry). It is the caller's input,
// not a service failure.
var ErrNoRoute = errors.New("valhalla: no route found")

// maxResponseBytes bounds how much of a Valhalla answer is read.
const maxResponseBytes = 8 << 20

// maxRouteLocations is Valhalla's default limit per request.
const maxRouteLocations = 50

// Route asks Valhalla for the auto route through locs (lat, lon pairs, at
// least two, every one a stop so each gap is its own leg) in one request,
// and returns per-leg length, time and shape. It sits beside RouteShape,
// which stays as it is.
func (v *Valhalla) Route(ctx context.Context, locs [][2]float64, opts RouteOpts) (Route, error) {
	if !v.Enabled() {
		return Route{}, errors.New("valhalla: not configured")
	}
	if len(locs) < 2 || len(locs) > maxRouteLocations {
		return Route{}, fmt.Errorf("valhalla: %d locations (need 2 to %d)", len(locs), maxRouteLocations)
	}
	ls := make([]map[string]any, len(locs))
	for i, l := range locs {
		ls[i] = map[string]any{"lat": l[0], "lon": l[1], "type": "break"}
	}
	body := map[string]any{
		"locations":          ls,
		"costing":            "auto",
		"shape_format":       "polyline6",
		"directions_options": map[string]any{"language": "en-US"},
	}
	// Valhalla's use_* factors run 0 (avoid as far as possible) to 1.
	co := map[string]any{}
	if opts.AvoidTolls {
		co["use_tolls"] = 0
	}
	if opts.AvoidFerries {
		co["use_ferry"] = 0
	}
	if opts.AvoidHighways {
		co["use_highways"] = 0
	}
	if len(co) > 0 {
		body["costing_options"] = map[string]any{"auto": co}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return Route{}, fmt.Errorf("valhalla: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+"/route", bytes.NewReader(buf))
	if err != nil {
		return Route{}, fmt.Errorf("valhalla: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return Route{}, fmt.Errorf("valhalla: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		// Valhalla answers a request it cannot route (no path, a location with
		// no road nearby) with 400 and an error body, not with a 5xx.
		return Route{}, ErrNoRoute
	}
	if resp.StatusCode/100 != 2 {
		return Route{}, fmt.Errorf("valhalla: HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Trip struct {
			Legs []struct {
				Summary struct {
					Length float64 `json:"length"` // km
					Time   float64 `json:"time"`   // s
				} `json:"summary"`
				Shape string `json:"shape"`
			} `json:"legs"`
		} `json:"trip"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&parsed); err != nil {
		return Route{}, fmt.Errorf("valhalla: decode: %w", err)
	}
	if len(parsed.Trip.Legs) != len(locs)-1 {
		return Route{}, fmt.Errorf("valhalla: %d legs for %d locations", len(parsed.Trip.Legs), len(locs))
	}
	out := Route{Legs: make([]RouteLeg, len(parsed.Trip.Legs))}
	for i, l := range parsed.Trip.Legs {
		shape := decodePolyline(l.Shape, 1e6)
		if len(shape) == 0 {
			return Route{}, errors.New("valhalla: empty shape")
		}
		out.Legs[i] = RouteLeg{LengthMeters: l.Summary.Length * 1000, TimeSec: l.Summary.Time, Shape: shape}
	}
	return out, nil
}
