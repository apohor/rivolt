package settings

import (
	"context"
	"errors"
	"math"
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
	keyHomeLocationLat     = "home_location.latitude"
	keyHomeLocationLon     = "home_location.longitude"
	keyHomeLocationLabel   = "home_location.label"
	keyHomeLocationAddress = "home_location.address"
	keyHomeLocationSource  = "home_location.source"
	// The home before the latest change, so a change can be undone (RestorePreviousHomeLocation).
	keyHomeLocationPrevLat     = "home_location.prev_latitude"
	keyHomeLocationPrevLon     = "home_location.prev_longitude"
	keyHomeLocationPrevLabel   = "home_location.prev_label"
	keyHomeLocationPrevAddress = "home_location.prev_address"
	keyHomeLocationPrevSource  = "home_location.prev_source"
)

// The ways a home can be set (HomeLocation.Source), for the Settings card only.
const (
	HomeSourceCity        = "city"
	HomeSourceAddress     = "address"
	HomeSourceCoordinates = "coordinates"
	HomeSourceCharge      = "charge"
)

// ErrHomeLocationInvalid: a latitude or longitude that isn't a finite value in range.
var ErrHomeLocationInvalid = errors.New("latitude must be between -90 and 90 and longitude between -180 and 180")

// ValidateHomeLocation checks a home about to be saved: finite, lat in [-90, 90], lon in [-180, 180], and a known
// source. A clear (Set=false) is always valid.
func ValidateHomeLocation(h HomeLocation) error {
	if !h.Set {
		return nil
	}
	if math.IsNaN(h.Latitude) || math.IsInf(h.Latitude, 0) || math.IsNaN(h.Longitude) || math.IsInf(h.Longitude, 0) ||
		h.Latitude < -90 || h.Latitude > 90 || h.Longitude < -180 || h.Longitude > 180 {
		return ErrHomeLocationInvalid
	}
	switch h.Source {
	case "", HomeSourceCity, HomeSourceAddress, HomeSourceCoordinates, HomeSourceCharge:
		return nil
	}
	return errors.New("unknown source")
}

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
	// Label is the short, user-visible name surfaced everywhere
	// the point is referenced (trip planner From: Home, drive
	// timeline "departed Home"). Defaults to "Home" on save when
	// the client doesn't provide one — keeps planner UI compact.
	Label string `json:"label,omitempty"`
	// Address is the full geocoded address kept for context on
	// the Settings card. Never used as the primary label.
	Address string `json:"address,omitempty"`
	// Source is how the home was set: "city", "address", "coordinates" or "charge" ("" before it was recorded).
	Source string `json:"source,omitempty"`
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
	if v, ok := all[keyHomeLocationAddress]; ok {
		h.Address = v
	}
	if v, ok := all[keyHomeLocationSource]; ok {
		h.Source = v
	}
	// Backfill: pre-Address writes stored the full geocoded
	// address in `label`. If the loaded label looks like an
	// address (contains a comma or > 30 chars) and Address is
	// empty, treat the label-as-address and rename the label.
	// Cheap one-pass migration for existing rows.
	if h.Address == "" && (len(h.Label) > 30 || stringsContains(h.Label, ",")) {
		h.Address = h.Label
		h.Label = "Home"
	}
	if h.Label == "" {
		h.Label = "Home"
	}
	return h, nil
}

// stringsContains avoids pulling the strings package import for one use.
func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// SetHomeLocation persists a home location (or clears it with Set=false), keeping the home it replaces as the
// previous one so the change can be undone.
func SetHomeLocation(ctx context.Context, s *Store, h HomeLocation) error {
	if s == nil {
		return nil
	}
	cur, err := GetHomeLocation(ctx, s)
	if err != nil {
		return err
	}
	if cur.Set && (!h.Set || cur.Latitude != h.Latitude || cur.Longitude != h.Longitude) {
		if err := writeHome(ctx, s, cur, keyHomeLocationPrevLat, keyHomeLocationPrevLon, keyHomeLocationPrevLabel, keyHomeLocationPrevAddress, keyHomeLocationPrevSource); err != nil {
			return err
		}
	}
	return writeHome(ctx, s, h, keyHomeLocationLat, keyHomeLocationLon, keyHomeLocationLabel, keyHomeLocationAddress, keyHomeLocationSource)
}

// PreviousHomeLocation is the home the latest change replaced (Set=false when there is none).
func PreviousHomeLocation(ctx context.Context, s *Store) (HomeLocation, error) {
	var h HomeLocation
	if s == nil {
		return h, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return h, err
	}
	lat, lerr := strconv.ParseFloat(all[keyHomeLocationPrevLat], 64)
	lon, oerr := strconv.ParseFloat(all[keyHomeLocationPrevLon], 64)
	if lerr != nil || oerr != nil || (lat == 0 && lon == 0) {
		return h, nil
	}
	return HomeLocation{Set: true, Latitude: lat, Longitude: lon, Label: all[keyHomeLocationPrevLabel],
		Address: all[keyHomeLocationPrevAddress], Source: all[keyHomeLocationPrevSource]}, nil
}

// ErrNoPreviousHome: there's no earlier home to go back to.
var ErrNoPreviousHome = errors.New("no previous home location")

// RestorePreviousHomeLocation swaps the current home and the previous one (so restoring twice returns to where it
// started). ErrNoPreviousHome when there's nothing to restore.
func RestorePreviousHomeLocation(ctx context.Context, s *Store) (HomeLocation, error) {
	prev, err := PreviousHomeLocation(ctx, s)
	if err != nil {
		return HomeLocation{}, err
	}
	if !prev.Set {
		return HomeLocation{}, ErrNoPreviousHome
	}
	if err := SetHomeLocation(ctx, s, prev); err != nil {
		return HomeLocation{}, err
	}
	return GetHomeLocation(ctx, s)
}

// writeHome stores h under the given keys; Set=false (or 0,0) stores empty values.
func writeHome(ctx context.Context, s *Store, h HomeLocation, kLat, kLon, kLabel, kAddress, kSource string) error {
	if !h.Set || (h.Latitude == 0 && h.Longitude == 0) {
		for _, k := range []string{kLat, kLon, kLabel, kAddress, kSource} {
			if err := s.Set(ctx, k, ""); err != nil {
				return err
			}
		}
		return nil
	}
	label := h.Label
	if label == "" {
		label = "Home"
	}
	for k, v := range map[string]string{
		kLat: strconv.FormatFloat(h.Latitude, 'f', -1, 64), kLon: strconv.FormatFloat(h.Longitude, 'f', -1, 64),
		kLabel: label, kAddress: h.Address, kSource: h.Source,
	} {
		if err := s.Set(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}
