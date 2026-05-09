package settings

import (
	"context"
	"strconv"
)

// Trip-planner default preferences. Applied when the SPA's per-trip
// form doesn't override them. Stored alongside the other per-install
// settings in the shared app_settings KV.
const (
	keyPlannerDefaultDriveMode = "planner.default_drive_mode"
	keyPlannerHasAdapter       = "planner.has_adapter"
)

// Valid driveMode values for Rivian's planTrip2 enum. Names cribbed
// from the home-assistant-rivian integration's DRIVE_MODE_MAP and
// confirmed against the live gateway. The mobile app's user-facing
// labels don't match the enum; mapping below.
//
//   EVERYDAY               → "All-Purpose"  (default)
//   DISTANCE               → "Conserve"     (range-extension)
//   SPORT                  → "Sport"
//   WINTER                 → "Snow"
//   OFF_ROAD_AUTO          → "All-Terrain"
//
// DISTANCE produces materially different plans (one fewer stop +
// shorter route on long trips) so the knob is load-bearing.
const (
	DriveModeEveryday    = "EVERYDAY"
	DriveModeDistance    = "DISTANCE"
	DriveModeSport       = "SPORT"
	DriveModeWinter      = "WINTER"
	DriveModeOffRoadAuto = "OFF_ROAD_AUTO"
)

// PlannerPrefs are the user's saved trip-planner defaults. The SPA
// pre-fills the per-trip form from these so the user doesn't have
// to pick the same drive mode every time.
type PlannerPrefs struct {
	// DriveMode is one of EVERYDAY / DISTANCE / SPORT / WINTER /
	// OFF_ROAD_AUTO. Empty string means "no default — let Rivian
	// pick" (the gateway's default is EVERYDAY, so empty and
	// EVERYDAY produce the same plan).
	DriveMode string `json:"drive_mode"`
	// HasAdapter — does the vehicle have the Tesla NACS adapter?
	// Pointer so absent/false/true are all distinguishable on the
	// wire; default is unset.
	HasAdapter *bool `json:"has_adapter,omitempty"`
}

// GetPlannerPrefs returns the saved planner defaults. Returns the
// zero-value PlannerPrefs when nothing has been configured.
func GetPlannerPrefs(ctx context.Context, s *Store) (PlannerPrefs, error) {
	var p PlannerPrefs
	if s == nil {
		return p, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return p, err
	}
	if v, ok := all[keyPlannerDefaultDriveMode]; ok && v != "" {
		switch v {
		case DriveModeEveryday, DriveModeDistance, DriveModeSport, DriveModeWinter, DriveModeOffRoadAuto:
			p.DriveMode = v
		}
	}
	if v, ok := all[keyPlannerHasAdapter]; ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			p.HasAdapter = &b
		}
	}
	return p, nil
}

// SetPlannerPrefs persists the defaults. Empty DriveMode clears the
// stored value; nil HasAdapter clears the stored value.
func SetPlannerPrefs(ctx context.Context, s *Store, p PlannerPrefs) error {
	if s == nil {
		return nil
	}
	dm := p.DriveMode
	switch dm {
	case "", DriveModeEveryday, DriveModeDistance, DriveModeSport, DriveModeWinter, DriveModeOffRoadAuto:
		// accepted
	default:
		dm = "" // unknown value silently cleared
	}
	if err := s.Set(ctx, keyPlannerDefaultDriveMode, dm); err != nil {
		return err
	}
	if p.HasAdapter == nil {
		return s.Set(ctx, keyPlannerHasAdapter, "")
	}
	return s.Set(ctx, keyPlannerHasAdapter, strconv.FormatBool(*p.HasAdapter))
}
