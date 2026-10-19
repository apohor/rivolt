package settings

import "context"

// keyAIConsent is the user's own switch for sending their trip and drive
// data to the AI provider. Absent or anything but "1" means off.
const keyAIConsent = "privacy.ai_advice"

// AIConsent reports whether the user turned AI advice on. Off by default, and
// off if the setting cannot be read.
func AIConsent(ctx context.Context, s *Store) bool {
	if s == nil {
		return false
	}
	all, err := s.GetAll(ctx)
	return err == nil && all[keyAIConsent] == "1"
}

// SetAIConsent records the choice.
func SetAIConsent(ctx context.Context, s *Store, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.Set(ctx, keyAIConsent, v)
}
