package dcfcrates

import (
	"regexp"
	"strings"
)

// The charger-network rule shared with the map
// (web/src/lib/chargerNetwork.ts): which network a free-form name means,
// and whether a nearby charging node agrees with the network a charge is
// known (or estimated) to be on. Both implementations must pass
// testdata/choose_pin_vectors.json; change a vector before either side.

// Network keys. "" is "no network recognised".
const (
	KeyTesla            = "tesla"
	KeyTeslaDestination = "tesla_destination"
	KeyRivian           = "rivian"
	KeyRivianWaypoints  = "rivian_waypoints"
	KeyElectrifyAmerica = "electrify_america"
	KeyEVgo             = "evgo"
	KeyChargePoint      = "chargepoint"
	KeyIONNA            = "ionna"
	KeyShellRecharge    = "shell_recharge"
)

var keyDisplay = map[string]string{
	KeyTesla:            "Tesla Supercharger",
	KeyTeslaDestination: "Tesla Destination",
	KeyRivian:           "Rivian Adventure Network",
	KeyRivianWaypoints:  "Rivian Waypoints",
	KeyElectrifyAmerica: "Electrify America",
	KeyEVgo:             "EVgo",
	KeyChargePoint:      "ChargePoint",
	KeyIONNA:            "IONNA",
	KeyShellRecharge:    "Shell Recharge",
}

// keyRules are tried in order; AC sub-brands come first because they
// share the parent brand's name.
var keyRules = []struct {
	re  *regexp.Regexp
	key string
}{
	{regexp.MustCompile(`\btesla\b.*\bdestination\b|\bdestination\b.*\btesla\b`), KeyTeslaDestination},
	{regexp.MustCompile(`\brivian\b.*\bwaypoints?\b|\bwaypoints?\b`), KeyRivianWaypoints},
	{regexp.MustCompile(`\btesla\b|supercharger`), KeyTesla},
	{regexp.MustCompile(`\brivian\b|adventure network|\bran\b`), KeyRivian},
	{regexp.MustCompile(`electrify\s*america|\bea\b`), KeyElectrifyAmerica},
	{regexp.MustCompile(`\bev\s*go\b|evgo`), KeyEVgo},
	{regexp.MustCompile(`charge\s*point|chargepoint`), KeyChargePoint},
	{regexp.MustCompile(`\bionna\b`), KeyIONNA},
	{regexp.MustCompile(`shell\s*recharge|\bshell\b`), KeyShellRecharge},
}

// NetworkKey reads a free-form network, operator, brand or name string.
func NetworkKey(parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	s := strings.ToLower(strings.Join(keep, " "))
	if strings.TrimSpace(s) == "" {
		return ""
	}
	for _, r := range keyRules {
		if r.re.MatchString(s) {
			return r.key
		}
	}
	return ""
}

// DisplayNetwork is a friendly canonical name, else the string itself.
func DisplayNetwork(known string) string {
	if k := NetworkKey(known); k != "" {
		return keyDisplay[k]
	}
	return strings.TrimSpace(known)
}

// PoiLike is a charging node's descriptive fields (OSM or the archive).
type PoiLike struct {
	Name       string  `json:"name,omitempty"`
	Operator   string  `json:"operator,omitempty"`
	Network    string  `json:"network,omitempty"`
	Brand      string  `json:"brand,omitempty"`
	MaxPowerKW float64 `json:"maxPowerKW,omitempty"`
}

// dcOnly networks only run DC fast chargers.
var dcOnly = map[string]bool{
	KeyTesla: true, KeyRivian: true, KeyElectrifyAmerica: true, KeyEVgo: true, KeyIONNA: true,
}

// IsDCOnlyNode reports a node an AC session can't be at.
func IsDCOnlyNode(poi *PoiLike) bool {
	if poi == nil {
		return false
	}
	if poi.MaxPowerKW >= 50 {
		return true
	}
	return dcOnly[NetworkKey(poi.Network, poi.Operator, poi.Brand, poi.Name)]
}

// PinChoice is what a charge's pin says: Snap uses the node as-is;
// otherwise the charge's own location is labelled.
type PinChoice struct {
	Snap  bool   `json:"snap"`
	Label string `json:"label"`
}

var unknownNetworks = map[string]bool{"": true, "unknown": true, "other": true, "n/a": true}

// ChoosePin decides whether a nearby node agrees with the known network
// (Snap) and what to call the charge. Never a wrong brand:
//   - a non-DC session never snaps to a DC-only node;
//   - unknown network: snap with the node's name, or a generic label;
//   - the node agrees: snap;
//   - it disagrees, or can't be verified (no network info): the known
//     network at the charge's own location.
func ChoosePin(poi *PoiLike, known string, isDC bool) PinChoice {
	known = strings.TrimSpace(known)
	unknown := unknownNetworks[strings.ToLower(known)]
	if !isDC && IsDCOnlyNode(poi) {
		if unknown {
			return PinChoice{Label: "Charging station"}
		}
		return PinChoice{Label: DisplayNetwork(known)}
	}
	snapped := "Charging station"
	if poi != nil && poi.Name != "" {
		snapped = poi.Name
	}
	if unknown {
		if poi != nil {
			return PinChoice{Snap: true, Label: snapped}
		}
		if isDC {
			return PinChoice{Label: "DC fast charger"}
		}
		return PinChoice{Label: "Charging station"}
	}
	if poi == nil {
		return PinChoice{Label: DisplayNetwork(known)}
	}
	want := NetworkKey(known)
	got := NetworkKey(poi.Network, poi.Operator, poi.Brand, poi.Name)
	if want != "" && got != "" && want == got {
		return PinChoice{Snap: true, Label: snapped}
	}
	if want == "" && got == "" {
		return PinChoice{Snap: true, Label: snapped}
	}
	return PinChoice{Label: DisplayNetwork(known)}
}
