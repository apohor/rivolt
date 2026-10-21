package appauth

import "time"

// SetNow replaces the clock (tests only).
func SetNow(s *Store, now func() time.Time) { s.now = now }

// SetMaxOutstanding lowers the in-flight cap (tests only).
func SetMaxOutstanding(s *Store, n int) { s.maxOutstanding = n }
