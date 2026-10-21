package appauth

import "time"

// SetNow replaces the clock (tests only).
func SetNow(s *Store, now func() time.Time) { s.now = now }
