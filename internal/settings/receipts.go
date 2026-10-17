package settings

import "context"

// keyRivianReceipts is the user's opt-in to reading their Rivian charging
// receipts (what Rivian billed). Absent or anything but "1" means off.
const keyRivianReceipts = "privacy.rivian_receipts"

// RivianReceiptsOptIn reports whether the user opted in. Off by default.
func RivianReceiptsOptIn(ctx context.Context, s *Store) bool {
	if s == nil {
		return false
	}
	all, err := s.GetAll(ctx)
	return err == nil && all[keyRivianReceipts] == "1"
}

// SetRivianReceiptsOptIn records the choice.
func SetRivianReceiptsOptIn(ctx context.Context, s *Store, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.Set(ctx, keyRivianReceipts, v)
}
