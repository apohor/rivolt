package settings

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Trip-planner default preferences. Applied when the SPA's per-trip
// form doesn't override them. Stored alongside the other per-install
// settings in the shared app_settings KV.
const (
	keyPlannerDefaultDriveMode = "planner.default_drive_mode"
	keyPlannerHasAdapter       = "planner.has_adapter"
	keyPlannerCorridor         = "planner.corridor"
	keyPlannerStartExcludeMi   = "planner.start_exclude_mi"
	keyPlannerMinFastKW        = "planner.min_fast_kw"
	keyPlannerLayersOn         = "planner.layers_on"
	keyPlannerPreferredOnly    = "planner.preferred_only"
	keyPlannerWeatherOn        = "planner.weather_on"
	keyPlannerRadarOn          = "planner.radar_on"
)

// The fixed choices of the Trip planner settings page. Free values are refused, so the clients render a picker and the corridor query takes an
// enum, never a number to size a query by.
var (
	// Corridors are the "how far from the route" presets (chargers.CorridorPreset has the widths).
	Corridors = []string{"close", "normal", "wide"}
	// StartExcludeMiles are the "hide chargers near the start" choices, measured along the route.
	StartExcludeMiles = []int{0, 25, 50}
	// MinFastKWs are the "minimum fast-charger power" choices; 0 is any.
	MinFastKWs = []int{0, 50, 150, 250}
	// LayerKinds are the map layers that can start on.
	LayerKinds = []string{"dcfc", "l2", "hotels", "camp"}
)

// The defaults GET fills in for a field the user never set.
const (
	DefaultCorridor       = "normal"
	DefaultStartExcludeMi = 25
	DefaultMinFastKW      = 0
	DefaultPreferredOnly  = false
	DefaultWeatherOn      = true
	DefaultRadarOn        = false
)

// DefaultLayersOn is the layers that start on for a user who never chose.
func DefaultLayersOn() []string { return []string{"dcfc"} }

// Valid driveMode values for Rivian's planTrip2 enum. Names cribbed
// from the home-assistant-rivian integration's DRIVE_MODE_MAP and
// confirmed against the live gateway. The mobile app's user-facing
// labels don't match the enum; mapping below.
//
//	EVERYDAY               → "All-Purpose"  (default)
//	DISTANCE               → "Conserve"     (range-extension)
//	SPORT                  → "Sport"
//	WINTER                 → "Snow"
//	OFF_ROAD_AUTO          → "All-Terrain"
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

// DriveModes is every drive mode the trip planner accepts, in the order above. The trip handlers drop anything else to "" before it reaches
// the planner, and the forwarder's plan_legs schema must accept exactly these (TestForwardedDriveModesMatchTheHandlers).
var DriveModes = []string{DriveModeEveryday, DriveModeDistance, DriveModeSport, DriveModeWinter, DriveModeOffRoadAuto}

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

	// The Trip planner settings page. Each is a pointer (nil for layers) so a PUT that leaves one out leaves it as it was: an older client that
	// sends only drive_mode and has_adapter never wipes them. GetPlannerPrefs leaves unset ones nil; WithDefaults fills them for the wire.
	Corridor       *string   `json:"corridor,omitempty"`
	StartExcludeMi *int      `json:"start_exclude_mi,omitempty"`
	MinFastKW      *int      `json:"min_fast_kw,omitempty"`
	LayersOn       *[]string `json:"layers_on,omitempty"`
	PreferredOnly  *bool     `json:"preferred_only,omitempty"`
	WeatherOn      *bool     `json:"weather_on,omitempty"`
	RadarOn        *bool     `json:"radar_on,omitempty"`
}

// WithDefaults returns p with every unset Trip planner settings field filled in, so a client needs no default logic.
func (p PlannerPrefs) WithDefaults() PlannerPrefs {
	if p.Corridor == nil {
		v := DefaultCorridor
		p.Corridor = &v
	}
	if p.StartExcludeMi == nil {
		v := DefaultStartExcludeMi
		p.StartExcludeMi = &v
	}
	if p.MinFastKW == nil {
		v := DefaultMinFastKW
		p.MinFastKW = &v
	}
	if p.LayersOn == nil {
		v := DefaultLayersOn()
		p.LayersOn = &v
	}
	if p.PreferredOnly == nil {
		v := DefaultPreferredOnly
		p.PreferredOnly = &v
	}
	if p.WeatherOn == nil {
		v := DefaultWeatherOn
		p.WeatherOn = &v
	}
	if p.RadarOn == nil {
		v := DefaultRadarOn
		p.RadarOn = &v
	}
	return p
}

// Validate refuses a value outside the fixed choices, naming the field. The drive mode is not checked here (an unknown one is cleared, as it
// always was).
func (p PlannerPrefs) Validate() error {
	if p.Corridor != nil && !slices.Contains(Corridors, *p.Corridor) {
		return fmt.Errorf("corridor must be one of %s", strings.Join(Corridors, ", "))
	}
	if p.StartExcludeMi != nil && !slices.Contains(StartExcludeMiles, *p.StartExcludeMi) {
		return fmt.Errorf("start_exclude_mi must be one of 0, 25, 50")
	}
	if p.MinFastKW != nil && !slices.Contains(MinFastKWs, *p.MinFastKW) {
		return fmt.Errorf("min_fast_kw must be one of 0, 50, 150, 250")
	}
	if p.LayersOn != nil {
		seen := map[string]bool{}
		for _, k := range *p.LayersOn {
			if !slices.Contains(LayerKinds, k) || seen[k] {
				return fmt.Errorf("layers_on must be a list of distinct values from %s", strings.Join(LayerKinds, ", "))
			}
			seen[k] = true
		}
	}
	return nil
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
	if v, ok := all[keyPlannerCorridor]; ok && slices.Contains(Corridors, v) {
		p.Corridor = &v
	}
	if v, ok := all[keyPlannerStartExcludeMi]; ok {
		if n, err := strconv.Atoi(v); err == nil && slices.Contains(StartExcludeMiles, n) {
			p.StartExcludeMi = &n
		}
	}
	if v, ok := all[keyPlannerMinFastKW]; ok {
		if n, err := strconv.Atoi(v); err == nil && slices.Contains(MinFastKWs, n) {
			p.MinFastKW = &n
		}
	}
	if v, ok := all[keyPlannerLayersOn]; ok {
		kinds := []string{}
		valid := true
		for _, k := range strings.Split(v, ",") {
			if k == "" {
				continue
			}
			if !slices.Contains(LayerKinds, k) || slices.Contains(kinds, k) {
				valid = false
				break
			}
			kinds = append(kinds, k)
		}
		if valid { // "" is a real choice: no layer starts on
			p.LayersOn = &kinds
		}
	}
	for key, dst := range map[string]**bool{keyPlannerPreferredOnly: &p.PreferredOnly, keyPlannerWeatherOn: &p.WeatherOn, keyPlannerRadarOn: &p.RadarOn} {
		if v, ok := all[key]; ok && v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = &b
			}
		}
	}
	return p, nil
}

// SetPlannerPrefs persists the defaults. Empty DriveMode clears the stored value; nil HasAdapter clears the stored value. The Trip planner
// settings fields are written only when set, in one SetMany with the two above, and refused (nothing written) when outside the fixed choices.
func SetPlannerPrefs(ctx context.Context, s *Store, p PlannerPrefs) error {
	if s == nil {
		return nil
	}
	if err := p.Validate(); err != nil {
		return err
	}
	dm := p.DriveMode
	switch dm {
	case "", DriveModeEveryday, DriveModeDistance, DriveModeSport, DriveModeWinter, DriveModeOffRoadAuto:
		// accepted
	default:
		dm = "" // unknown value silently cleared
	}
	kv := map[string]string{keyPlannerDefaultDriveMode: dm, keyPlannerHasAdapter: ""}
	if p.HasAdapter != nil {
		kv[keyPlannerHasAdapter] = strconv.FormatBool(*p.HasAdapter)
	}
	if p.Corridor != nil {
		kv[keyPlannerCorridor] = *p.Corridor
	}
	if p.StartExcludeMi != nil {
		kv[keyPlannerStartExcludeMi] = strconv.Itoa(*p.StartExcludeMi)
	}
	if p.MinFastKW != nil {
		kv[keyPlannerMinFastKW] = strconv.Itoa(*p.MinFastKW)
	}
	if p.LayersOn != nil {
		kv[keyPlannerLayersOn] = strings.Join(*p.LayersOn, ",")
	}
	for key, v := range map[string]*bool{keyPlannerPreferredOnly: p.PreferredOnly, keyPlannerWeatherOn: p.WeatherOn, keyPlannerRadarOn: p.RadarOn} {
		if v != nil {
			kv[key] = strconv.FormatBool(*v)
		}
	}
	return s.SetMany(ctx, kv)
}
