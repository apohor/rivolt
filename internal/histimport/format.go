package histimport

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Kind is what a table's rows are.
type Kind int

const (
	KindDrives  Kind = iota // one row per drive
	KindCharges             // one row per charge
	KindPoints              // one row per recorded point; drives and charges are derived from runs of points
)

// Dim is the physical dimension of a field, which says what its header unit can be.
type Dim int

const (
	DimText Dim = iota
	DimTime
	DimDistance // km or mi
	DimSpeed    // km/h or mph
	DimEnergy   // kWh or Wh
	DimPower    // kW or W
	DimSoC      // percent, or a fraction
	DimAngle
	DimMoney
)

// Field is one canonical field and the header spellings that mean it. Aliases are compared after normalising (see baseName): case, spaces,
// punctuation and any unit word are ignored, so "Distance (km)", "distance_km" and "DISTANCE" are all "distance".
type Field struct {
	Name     string
	Aliases  []string
	Dim      Dim
	Required bool
}

// Format describes one export file: which source it comes from, what its rows are, and its columns. Formats are data, so a corrected or
// additional spelling is a one-line change (slice 2b pins them to real exports).
type Format struct {
	Source      string // "tezlab_import", "abrp_import": the Source of the rows it produces
	Kind        Kind
	Fields      []Field
	EnergyBasis string // what the file's charge energy measures: "battery", "wall" or "unknown" (recorded on the row, never guessed)
}

// Canonical field names.
const (
	FStart, FEnd         = "start", "end"
	FTime                = "time"
	FSessionID           = "session"
	FDistance            = "distance"
	FStartSoC, FEndSoC   = "start_soc", "end_soc"
	FSoC                 = "soc"
	FStartOdo, FEndOdo   = "start_odometer", "end_odometer"
	FOdometer            = "odometer"
	FStartLat, FStartLon = "start_lat", "start_lon"
	FEndLat, FEndLon     = "end_lat", "end_lon"
	FLat, FLon           = "lat", "lon"
	FMaxSpeed, FAvgSpeed = "max_speed", "avg_speed"
	FSpeed               = "speed"
	FEnergyUsed          = "energy_used"
	FEnergyAdded         = "energy_added"
	FMaxPower, FAvgPower = "max_power", "avg_power"
	FPower               = "power"
	FCost, FCurrency     = "cost", "currency"
	FVehicle             = "vehicle"
)

// unit words that may sit in a header next to the name; they are removed to get the base name and read as the unit hint.
var unitWords = map[string]string{
	"km": "km", "kms": "km", "kilometer": "km", "kilometers": "km", "kilometre": "km", "kilometres": "km",
	"mi": "mi", "mile": "mi", "miles": "mi",
	"kph": "kph", "kmh": "kph", "mph": "mph",
	"kwh": "kwh", "wh": "wh",
	"kw": "kw", "w": "w",
	"pct": "pct", "percent": "pct", "fraction": "frac",
}

// splitHeader returns a header's base name and the unit hint in it ("" when it states none).
func splitHeader(h string) (base, unit string) {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.NewReplacer("km/h", " kph ", "mi/h", " mph ", "%", " pct ").Replace(h)
	var b strings.Builder
	for _, tok := range strings.FieldsFunc(h, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if u, ok := unitWords[tok]; ok {
			unit = u
			continue
		}
		b.WriteString(tok)
	}
	return b.String(), unit
}

func baseName(s string) string { b, _ := splitHeader(s); return b }

// column is a field matched to a header.
type column struct {
	idx  int
	unit string
	f    Field
}

// MissingColumnsError refuses a file with no header for a required field. It carries header names only, never row values, so it can be
// shown to the user and sent to support.
type MissingColumnsError struct {
	Missing []string // canonical names of the required fields not found
	Header  []string
}

func (e *MissingColumnsError) Error() string {
	return "histimport: this file has no column for: " + strings.Join(e.Missing, ", ")
}

// resolve matches the table's header to the format's fields. An unmatched header is ignored (exports carry many columns we do not use).
// The first header that matches a field wins; a second match for the same field is ignored, so a "Distance" next to a "Distance (km)"
// cannot change meaning with column order across files only if the file itself changes.
func resolve(header []string, f Format) (map[string]column, error) {
	byAlias := map[string]Field{}
	for _, fld := range f.Fields {
		for _, a := range fld.Aliases {
			byAlias[baseName(a)] = fld
		}
	}
	cols := map[string]column{}
	for i, h := range header {
		base, unit := splitHeader(h)
		fld, ok := byAlias[base]
		if !ok || base == "" {
			continue
		}
		if _, dup := cols[fld.Name]; dup {
			continue
		}
		cols[fld.Name] = column{idx: i, unit: unit, f: fld}
	}
	var missing []string
	for _, fld := range f.Fields {
		if fld.Required {
			if _, ok := cols[fld.Name]; !ok {
				missing = append(missing, fld.Name)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &MissingColumnsError{Missing: missing, Header: append([]string(nil), header...)}
	}
	return cols, nil
}

// NeedChoiceError: the file does not say something the numbers depend on, so the user must (the preview asks). Never guessed.
type NeedChoiceError struct {
	Choices  []string // "distance_unit" (km|mi), "energy_unit" (kwh|wh), "timezone", "vehicle"
	Vehicles []string // for "vehicle": the labels the file holds
}

func (e *NeedChoiceError) Error() string {
	return fmt.Sprintf("histimport: the file does not say its %s", strings.Join(e.Choices, ", "))
}
