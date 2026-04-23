package rivian

import (
	"context"
	"fmt"
	"strings"
)

// DefaultPackKWh is the usable capacity we fall back to when trim /
// model are unknown. It matches the R1T/R1S Large pack and is a
// reasonable default for Rivolt's current owner base.
const DefaultPackKWh = 141.5

// InferPackKWh maps a (model, trim-option-id, modelYear) tuple to the
// best-known usable battery capacity in kWh. Data comes from Rivian's
// owner-facing spec sheets and community teardowns — the API itself
// does not expose pack size. Unknown combinations return
// DefaultPackKWh so the SoC-delta fallback keeps working.
//
// Rivian trim optionId strings observed in the wild:
//
//	Gen 1 (2022–2024):
//	  "LRG-DM-PRFM"  R1T/R1S Large Dual-Motor Performance
//	  "LRG-DM-STD"   R1T/R1S Large Dual-Motor Standard
//	  "LRG-QM"       R1T/R1S Large Quad-Motor
//	  "STD-DM"       R1T/R1S Standard Dual-Motor (Gen1 "Standard" = ~105 kWh)
//	  "MAX-DM"       R1T/R1S Max pack Dual-Motor
//	  "MAX-QM"       R1T/R1S Max pack Quad-Motor
//	Gen 2 (2025+):
//	  "G2-STD-DM"    Gen2 Standard+ (~92.5 kWh usable)
//	  "G2-LRG-DM"    Gen2 Large (~141.5 kWh usable)
//	  "G2-MAX-DM"    Gen2 Max (~180 kWh usable)
//	  "G2-TRI-MTR"   Gen2 Tri-Motor
//	  "G2-QUAD"      Gen2 Quad-Motor (Max pack)
//	Pre-Gen1 / early trims also show package codes (PKG-ADV, PKG-AV)
//	which do NOT encode pack size — in that case we fall through to
//	model-only defaults.
//
// The exact usable kWh numbers are approximate. For a more accurate
// fallback, let the operator override via a setting later.
func InferPackKWh(model, trimID string, modelYear int) float64 {
	t := strings.ToUpper(strings.TrimSpace(trimID))
	// Explicit pack-size tokens take precedence.
	switch {
	case strings.Contains(t, "MAX"):
		return 180.0
	case strings.Contains(t, "LRG") || strings.Contains(t, "LARGE"):
		return 141.5
	case strings.HasPrefix(t, "G2-STD") || strings.Contains(t, "STANDARD-PLUS") || strings.Contains(t, "STD-PLUS"):
		// Gen2 Standard+ ~92.5 kWh usable
		return 92.5
	case strings.HasPrefix(t, "STD") || strings.Contains(t, "STANDARD"):
		// Gen1 Standard pack ~105 kWh usable (LFP on some builds)
		return 105.0
	}
	// Model-only defaults (no pack encoded in trim, e.g. PKG-ADV).
	m := strings.ToUpper(strings.TrimSpace(model))
	switch m {
	case "R1T", "R1S":
		// Most Gen1 owners bought Large. Safer default than Max.
		return 141.5
	case "R2":
		// R2 ships with a single standard pack ~75 kWh (spec).
		return 75.0
	}
	return DefaultPackKWh
}

// qVehicleImages pulls pre-rendered mobile images for the vehicle the
// user has configured. Rivian hosts a couple of versions ("1" = early
// render, "2" = current configurator output). We request v2 PNGs at
// @2x, which is what the owner app renders on a phone.
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

// VehicleImages returns the configurator-rendered mobile images for
// every vehicle on the account. Filter on VehicleID client-side.
// Returns an empty slice (not an error) if Rivian has no images yet.
func (c *LiveClient) VehicleImages(ctx context.Context) ([]VehicleImage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.userSessionToken == "" {
		return nil, fmt.Errorf("rivian: not authenticated; call Login first")
	}
	data, err := doGraphQL[vehicleImagesData](ctx, c, graphQLRequest{
		OperationName: "getVehicleImages",
		Query:         qVehicleImages,
		Variables: vehicleImagesVars{
			Extension:      "png",
			Resolution:     "@2x",
			VersionVehicle: "2",
		},
	}, c.authHeaders())
	if err != nil {
		return nil, fmt.Errorf("getVehicleImages: %w", err)
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
