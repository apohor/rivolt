package rivian

import (
	"context"
	"fmt"
	"strings"
)

// DefaultPackKWh is the usable capacity we fall back to when trim /
// model are unknown. It matches the R1T/R1S Gen 1 Large pack
// (131 kWh usable per Rivian spec) and is a reasonable default for
// Rivolt's current owner base.
const DefaultPackKWh = 131.0

// InferPackKWh maps a (model, trim-option-id, modelYear) tuple to the
// best-known USABLE battery capacity in kWh, to seed a truck until it reports
// its own (the vehicle's batteryCapacity replaces this on its first frame, and
// that is what drives and overnight charging use). The GraphQL API exposes
// model / modelYear / trimOption.optionId but NOT pack size. Unknown
// combinations return DefaultPackKWh.
//
// The figures are usable, not gross. Rivian does not publish a usable figure;
// these are the numbers owners and the press measured and compiled (Rivian
// Forums "battery size Gen 1 vs Gen 2", InsideEVs on the Max pack, 2026-10):
//
//	           Gen 1 usable (gross)     Gen 2 usable
//	Standard   106 (105)                92.5
//	Standard+  121                      (92.5, named "Standard+" in some trim ids)
//	Large      131 (135)                109.4
//	Max        141 (149)                141.5
//
// An earlier version had Max at 180 kWh, which is neither the usable (141) nor
// the gross (149) figure, and Gen 2 Large at 141.5 (that is the Max pack).
//
// The optionIds seen on real trucks (prod, 2026-10) are "PKG-LCH", "PKG-ADV",
// "PKG-ASC", "BLD-DR1T", "BLD-TR1S" and "BLD-QR1S": they do not encode the pack,
// so most trucks reach the model-only default below. For a Gen 2 R1 that default
// is the Max figure, and a Standard or Large Gen 2 owner sees it until the truck
// reports its own capacity. Pack-size tokens in the trim ("MAX", "LRG", "STD")
// win when present.
//
// Model year >= 2025 or a trim prefix of "G2-" indicates Gen 2.
func InferPackKWh(model, trimID string, modelYear int) float64 {
	t := strings.ToUpper(strings.TrimSpace(trimID))
	gen2 := modelYear >= 2025 || strings.HasPrefix(t, "G2-")
	// Explicit pack-size tokens take precedence.
	switch {
	case strings.Contains(t, "STD-PLUS") || strings.Contains(t, "STANDARD-PLUS"):
		if gen2 {
			return 92.5
		}
		return 121.0
	case strings.Contains(t, "MAX"):
		if gen2 {
			return 141.5
		}
		return 141.0
	case strings.Contains(t, "LRG") || strings.Contains(t, "LARGE"):
		if gen2 {
			return 109.4
		}
		return 131.0
	case strings.HasPrefix(t, "G2-STD"):
		return 92.5
	case strings.HasPrefix(t, "STD") || strings.Contains(t, "STANDARD"):
		return 106.0
	}
	// Model-only defaults (no pack encoded in trim, e.g. PKG-ADV).
	m := strings.ToUpper(strings.TrimSpace(model))
	switch m {
	case "R1T", "R1S":
		if gen2 {
			return 141.5
		}
		return 131.0
	case "R2":
		// R2: the two in the fleet (2027 MY, new) report 91.47 and
		// 91.64 kWh; R1 Gen2 trucks report ~101% of their 141.5 spec,
		// so the pack is ~91.5. (75 was a pre-launch guess: pack health
		// read 122%.)
		return 91.5
	}
	return DefaultPackKWh
}

// qVehicleImages pulls pre-rendered mobile images for the vehicle the
// user has configured. Rivian hosts several render versions ("1" = early,
// "2" = configurator output, "3" = what the app 3.17.0 requests); the
// variables we send are in vehicleImageAttempts.
const qVehicleImages = `query getVehicleImages($extension: String, $resolution: String, $versionVehicle: String) {
  getVehicleMobileImages(resolution: $resolution, extension: $extension, version: $versionVehicle) {
    __typename
    vehicleId
    orderId
    url
    extension
    resolution
    size
    design
    placement
  }
}`

type vehicleImagesVars struct {
	Extension      string `json:"extension"`
	Resolution     string `json:"resolution"`
	VersionVehicle string `json:"versionVehicle"`
}

type vehicleImagesData struct {
	GetVehicleMobileImages []struct {
		VehicleID  string `json:"vehicleId"`
		OrderID    string `json:"orderId"`
		URL        string `json:"url"`
		Extension  string `json:"extension"`
		Resolution string `json:"resolution"`
		Size       string `json:"size"`
		Design     string `json:"design"`
		Placement  string `json:"placement"`
	} `json:"getVehicleMobileImages"`
}

// VehicleImage is one pre-rendered configurator image.
type VehicleImage struct {
	VehicleID  string `json:"vehicle_id"`
	OrderID    string `json:"order_id,omitempty"`
	URL        string `json:"url"`
	Extension  string `json:"extension,omitempty"`
	Resolution string `json:"resolution,omitempty"`
	Size       string `json:"size,omitempty"`
	Design     string `json:"design,omitempty"`
	Placement  string `json:"placement,omitempty"`
}

// vehicleImageAttempts are the variable sets tried in order. The Rivian
// Android app 3.17.0 asks for extension "webp", a density bucket
// (hdpi/xhdpi/xxhdpi/xxxhdpi) and version "3"; we used png, "@2x" and "2",
// and vehicles.image_url stayed empty for every vehicle. Try the app's
// request first and keep the old one as a fallback.
var vehicleImageAttempts = []vehicleImagesVars{
	{Extension: "webp", Resolution: "xxhdpi", VersionVehicle: "3"},
	{Extension: "png", Resolution: "@2x", VersionVehicle: "2"},
}

// VehicleImages returns the configurator-rendered mobile images for
// every vehicle on the account. Filter on VehicleID client-side.
// Returns an empty slice (not an error) if Rivian has no images yet.
func (c *LiveClient) VehicleImages(ctx context.Context) ([]VehicleImage, error) {
	var lastErr error
	for _, vars := range vehicleImageAttempts {
		// Headers per attempt: a heal in the previous one may have replaced them.
		headers, err := c.sessionHeaders()
		if err != nil {
			return nil, err
		}
		data, err := doGraphQLDetached[vehicleImagesData](ctx, c, graphQLRequest{
			OperationName: "getVehicleImages",
			Query:         qVehicleImages,
			Variables:     vars,
		}, headers)
		if err != nil {
			lastErr = fmt.Errorf("getVehicleImages: %w", err)
			continue
		}
		if len(data.GetVehicleMobileImages) == 0 {
			continue
		}
		out := make([]VehicleImage, 0, len(data.GetVehicleMobileImages))
		for _, img := range data.GetVehicleMobileImages {
			out = append(out, VehicleImage{
				VehicleID:  img.VehicleID,
				OrderID:    img.OrderID,
				URL:        img.URL,
				Extension:  img.Extension,
				Resolution: img.Resolution,
				Size:       img.Size,
				Design:     img.Design,
				Placement:  img.Placement,
			})
		}
		return out, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return []VehicleImage{}, nil
}

// Field support levels, as /api/vehicles reports them in field_support.
const (
	// FieldReported: the vehicle sends it and we show it.
	FieldReported = "reported"
	// FieldEstimate: derived or best guess; the UI labels it "(estimate)".
	FieldEstimate = "estimate"
	// FieldBestEffort: usually there, can lag or be missing for a while.
	FieldBestEffort = "best_effort"
	// FieldUnavailable: not sent, or not confirmed; the UI hides it.
	FieldUnavailable = "unavailable"
)

// FieldSupport says, per model, which live fields can be trusted, so the UI
// labels and hides from one place instead of checking models. One table to edit
// when a field is confirmed on a model or an R2 software change moves it (the
// evidence is in scratchpad r2-verified-design.md). Unknown models are treated
// as R1s. The map is a fresh copy.
func FieldSupport(model string) map[string]string {
	r2 := strings.EqualFold(strings.TrimSpace(model), "R2")
	out := map[string]string{
		// vehicleState carries a time to full on R1; an R2 gets Parallax's countdown.
		"charge_time_remaining": FieldReported,
		// R1 reports it on every frame; an R2's comes from Parallax and can lag
		// for a minute after a reconnect.
		"power_state": FieldReported,
		// vehicle_state.outside_temp_c is 0 on every row of every truck: never sourced.
		"outside_temp": FieldUnavailable,
		// Sent as a sentinel only on the R2s checked; R1 reports a trip target.
		"trip_target": FieldReported,
		// Decoded for neither model: constant or unconfirmed on the R2s.
		"charge_stop_reason": FieldUnavailable,
		"user_modes":         FieldUnavailable,
		"parked_energy":      FieldUnavailable,
		"smart_charging":     FieldUnavailable,
	}
	if r2 {
		out["charge_time_remaining"] = FieldEstimate
		out["power_state"] = FieldBestEffort
		out["trip_target"] = FieldUnavailable
	}
	return out
}
