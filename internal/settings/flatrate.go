package settings

import "context"

// keyChargingFlatRate is "my electricity is one flat rate, there are no off-peak
// hours": a bool in the charging namespace of the user's settings. It goes with the
// account, stays through a history reset, and is in the settings export and backup.
// Turning it on only stops the estimates that need off-peak hours (the savings card,
// the share card's off-peak line, the statement email's saving); the off-peak
// schedule, if there is one, stays stored, and charges are still priced at the home rate.
const keyChargingFlatRate = "charging.flat_rate"

// GetFlatRate reports whether the user said their rate is flat. False when unset.
func GetFlatRate(ctx context.Context, s *Store) (bool, error) {
	if s == nil {
		return false, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return false, err
	}
	return all[keyChargingFlatRate] == "1", nil
}

// SetFlatRate saves the choice; off removes the key (the default).
func SetFlatRate(ctx context.Context, s *Store, on bool) error {
	if s == nil {
		return nil
	}
	if !on {
		return s.Delete(ctx, keyChargingFlatRate)
	}
	return s.Set(ctx, keyChargingFlatRate, "1")
}
