package settings

import (
	"reflect"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

func fp(v float64) *float64 { return &v }

func TestNetworkLocation(t *testing.T) {
	ok := []ChargingNetwork{{Name: "Office", PricePerKWh: 0.2, Lat: fp(30.27), Lon: fp(-97.74)}, {Name: "Plain", PricePerKWh: 0.3}}
	if err := ValidateNetworkLocations(ok); err != nil {
		t.Errorf("valid: %v", err)
	}
	for _, bad := range []ChargingNetwork{
		{Name: "Half", Lat: fp(30)},
		{Name: "Half", Lon: fp(-97)},
		{Name: "Range", Lat: fp(91), Lon: fp(0)},
		{Name: "Range", Lat: fp(0), Lon: fp(-181)},
		{Name: "Null island", Lat: fp(0), Lon: fp(0)},
	} {
		if err := ValidateNetworkLocations([]ChargingNetwork{bad}); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}

	// Kept on a row of the user's own (rounded), dropped from a built-in network.
	builtin := dcfcrates.Networks[0]
	out := normalizeNetworks([]ChargingNetwork{
		{Name: "Office", PricePerKWh: 0.2, Lat: fp(30.2712345678), Lon: fp(-97.74)},
		{Name: builtin.DisplayName, Slug: builtin.Slug, Lat: fp(30.27), Lon: fp(-97.74)},
	})
	for _, n := range out {
		switch n.Slug {
		case "":
			if n.Lat == nil || *n.Lat != 30.271235 || n.Lon == nil {
				t.Errorf("own row location = %v %v", n.Lat, n.Lon)
			}
		default:
			if n.Lat != nil || n.Lon != nil {
				t.Errorf("built-in row kept a location")
			}
		}
	}
}

// A network's location is the user's alone: nothing that leaves their account or
// feeds a shared price book has a field to carry it.
func TestNetworkLocationStaysPrivate(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(dcfcrates.NetworkOverride{}), reflect.TypeOf(dcfcrates.Network{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(name, "lat") || strings.Contains(name, "lon") || strings.Contains(name, "location") {
				t.Errorf("%s.%s could carry a network location", typ.Name(), typ.Field(i).Name)
			}
		}
	}
	for _, n := range DefaultChargingNetworks() {
		if n.Lat != nil || n.Lon != nil {
			t.Errorf("default %s has a location", n.Name)
		}
	}
}
