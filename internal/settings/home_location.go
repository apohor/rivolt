package settings

import (
	"context"
	"strconv"
)

// Keys for the user's saved home location. Stored alongside the
// other per-install settings in the shared app_settings KV.
//
// Single-tenant assumption: the settings.Store is currently scoped
// per Rivolt instance, not per user; a multi-user roll-out would key
// these on (user_id, key). Today's deploy is single-user so the flat
// key works.
const (
	keyHomeLocationLat   = "home_location.latitude"
	keyHomeLocationLon   = "home_location.longitude"
	keyHomeLocationLabel = "home_location.label"
)

// HomeLocation is the user's "home" base — used by the trip planner
// to offer a one-click Origin / Destination preset, and (eventually)
// by the recap path to recognise round trips that end at home.
//
// Set is the boolean the UI uses to decide whether to render the
// Home preset button at all. Latitude/Longitude are the resolved
// coords; Label is the human-readable name (city + state) so the UI
// doesn't need to reverse-geocode every time.
type HomeLocation struct {
	Set       bool    `json:"set"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Label     string  `json:"label,omitempty"`
}

// GetHomeLocation reads the saved home location. Returns Set=false
// when no location has been configured (zero-value HomeLocation).
func GetHomeLocation(ctx context.Context, s *Store) (HomeLocation, error) {
	var h HomeLocation
	if s == nil {
		return h, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return h, err
	}
	latStr, latOK := all[keyHomeLocationLat]
	lonStr, lonOK := all[keyHomeLocationLon]
	if !latOK || !lonOK || latStr == "" || lonStr == "" {
		return h, nil
	}
	lat, lerr := strconv.ParseFloat(latStr, 64)
	lon, oerr := strconv.ParseFloat(lonStr, 64)
	if lerr != nil || oerr != nil {
		return h, nil
	}
	if lat == 0 && lon == 0 {
		return h, nil
	}
	h.Set = true
	h.Latitude = lat
	h.Longitude = lon
	if v, ok := all[keyHomeLocationLabel]; ok {
		h.Label = v
	}
	return h, nil
}

// SetHomeLocation persists a home location. Pass an empty
// HomeLocation (Set=false) to clear it.
func SetHomeLocation(ctx context.Context, s *Store, h HomeLocation) error {
	if s == nil {
		return nil
	}
	if !h.Set || (h.Latitude == 0 && h.Longitude == 0) {
		// Clear by writing empty values; downstream Get returns Set=false.
		if err := s.Set(ctx, keyHomeLocationLat, ""); err != nil {
			return err
		}
		if err := s.Set(ctx, keyHomeLocationLon, ""); err != nil {
			return err
		}
		return s.Set(ctx, keyHomeLocationLabel, "")
	}
	if err := s.Set(ctx, keyHomeLocationLat, strconv.FormatFloat(h.Latitude, 'f', -1, 64)); err != nil {
		return err
	}
	if err := s.Set(ctx, keyHomeLocationLon, strconv.FormatFloat(h.Longitude, 'f', -1, 64)); err != nil {
		return err
	}
	return s.Set(ctx, keyHomeLocationLabel, h.Label)
}
