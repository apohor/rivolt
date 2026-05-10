package maps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
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
func decodePolyline5(s string) [][2]float64 {
	if s == "" {
		return nil
	}
	var (
		out                [][2]float64
		i, lat, lon, shift int
		result             int32
	)
	for i < len(s) {
		shift = 0
		result = 0
		for {
			b := int32(s[i]) - 63
			i++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		var dlat int32
		if result&1 != 0 {
			dlat = ^(result >> 1)
		} else {
			dlat = result >> 1
		}
		lat += int(dlat)

		shift = 0
		result = 0
		for {
			b := int32(s[i]) - 63
			i++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		var dlon int32
		if result&1 != 0 {
			dlon = ^(result >> 1)
		} else {
			dlon = result >> 1
		}
		lon += int(dlon)

		out = append(out, [2]float64{float64(lat) / 1e5, float64(lon) / 1e5})
	}
	return out
}
