package settings

import "context"

// keyAmbientTemp is the user's opt-in to looking up the outside temperature from
// Open-Meteo for their truck's location (rounded to about 11 km). Rivian reports
// none. Separate from the drive-weather toggle: that one consents to a drive's
// start coordinates, this one to the truck's current location. Absent or
// anything but "1" means off.
const keyAmbientTemp = "privacy.ambient_temp"

// AmbientTempOptIn reports whether the user turned the lookup on. Off by default,
// and off if the setting cannot be read.
func AmbientTempOptIn(ctx context.Context, s *Store) bool {
	if s == nil {
		return false
	}
	all, err := s.GetAll(ctx)
	return err == nil && all[keyAmbientTemp] == "1"
}

// SetAmbientTempOptIn records the choice.
func SetAmbientTempOptIn(ctx context.Context, s *Store, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.Set(ctx, keyAmbientTemp, v)
}
