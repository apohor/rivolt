package settings

import (
	"context"
	"errors"
)

// The account's display units: distance (mi or km) and temperature (°C or °F), for the web and every iPhone screen. The server
// stores and serves SI everywhere; this only says how a client should show it. Stored in the per-user settings, so it follows
// the account across browsers and the phone.
//
// Distance has a source: "user" (the person chose), "region" (a client seeded it once from its region on first sign-in) or
// "unset". Temperature is "user" once chosen; until then it follows distance (km gives °C, mi gives °F), the rule the web has
// always used.
const (
	keyDistanceUnit    = "display.distance_unit"
	keyTemperatureUnit = "display.temperature_unit"
	keyUnitsSource     = "display.units_source"
)

// Unit values and sources.
const (
	DistanceMi = "mi"
	DistanceKm = "km"
	TempC      = "c"
	TempF      = "f"
	// TempAuto asks for temperature to follow distance again.
	TempAuto = "auto"

	SourceUser   = "user"
	SourceRegion = "region"
	SourceUnset  = "unset"
	SourceFollow = "follows_distance"
)

// Units is the resolved setting: always concrete values, with where each came from.
type Units struct {
	DistanceUnit      string `json:"distance_unit"`
	TemperatureUnit   string `json:"temperature_unit"`
	DistanceSource    string `json:"distance_source"`
	TemperatureSource string `json:"temperature_source"`
}

func validDistance(v string) bool { return v == DistanceMi || v == DistanceKm }
func validTemp(v string) bool     { return v == TempC || v == TempF }

// defaultTemp is the temperature unit that goes with a distance unit.
func defaultTemp(distance string) string {
	if distance == DistanceKm {
		return TempC
	}
	return TempF
}

// GetUnits resolves the account's units. An account with nothing stored reads mi and °F with source "unset" (the value is a
// placeholder: a client seeds it from its own region, see SeedDistanceUnit). A nil store, or one that cannot be read, reads
// the same.
func GetUnits(ctx context.Context, s *Store) (Units, error) {
	u := Units{DistanceUnit: DistanceMi, DistanceSource: SourceUnset, TemperatureSource: SourceFollow}
	if s != nil {
		all, err := s.GetAll(ctx)
		if err != nil {
			u.TemperatureUnit = defaultTemp(u.DistanceUnit)
			return u, err
		}
		if d := all[keyDistanceUnit]; validDistance(d) {
			u.DistanceUnit = d
			u.DistanceSource = SourceUser
			if all[keyUnitsSource] == SourceRegion {
				u.DistanceSource = SourceRegion
			}
		}
		if t := all[keyTemperatureUnit]; validTemp(t) {
			u.TemperatureUnit, u.TemperatureSource = t, SourceUser
			return u, nil
		}
	}
	u.TemperatureUnit = defaultTemp(u.DistanceUnit)
	return u, nil
}

// ErrBadUnit is a unit value outside the lists.
var ErrBadUnit = errors.New("settings: unknown unit")

// PatchUnits records the person's own choice. distance and temperature are each nil to leave alone; temperature may be
// TempAuto to follow distance again. A distance choice is source "user" and replaces a seeded one.
func PatchUnits(ctx context.Context, s *Store, distance, temperature *string) error {
	if s == nil {
		return errors.New("settings: store unavailable")
	}
	if distance != nil && !validDistance(*distance) {
		return ErrBadUnit
	}
	if temperature != nil && !validTemp(*temperature) && *temperature != TempAuto {
		return ErrBadUnit
	}
	kv := map[string]string{}
	if distance != nil {
		kv[keyDistanceUnit], kv[keyUnitsSource] = *distance, SourceUser
	}
	if temperature != nil && *temperature != TempAuto {
		kv[keyTemperatureUnit] = *temperature
	}
	if len(kv) > 0 {
		if err := s.SetMany(ctx, kv); err != nil {
			return err
		}
	}
	if temperature != nil && *temperature == TempAuto {
		return s.Delete(ctx, keyTemperatureUnit)
	}
	return nil
}

// SeedDistanceUnit records a client's regional default, once: only if the account has no distance unit yet (the first writer
// wins, so two clients signing in together agree). It reports whether this call wrote it. A choice the person makes later
// always replaces it.
func SeedDistanceUnit(ctx context.Context, s *Store, distance string) (bool, error) {
	if s == nil {
		return false, errors.New("settings: store unavailable")
	}
	if !validDistance(distance) {
		return false, ErrBadUnit
	}
	ns := namespaceFor(keyDistanceUnit)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO user_settings (user_id, namespace, value)
		VALUES ($1, $2, jsonb_build_object($3::text, $4::text, $5::text, $6::text))
		ON CONFLICT (user_id, namespace) DO UPDATE
			SET value = user_settings.value || jsonb_build_object($3::text, $4::text, $5::text, $6::text),
			    updated_at = NOW()
			WHERE NOT jsonb_exists(user_settings.value, $3::text)`,
		s.userID, ns, keyDistanceUnit, distance, keyUnitsSource, SourceRegion)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
