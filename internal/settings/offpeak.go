package settings

import (
	"context"
	"encoding/json"

	"github.com/apohor/rivolt/internal/offpeak"
)

// keyChargingOffpeak holds the user's electricity schedule (internal/offpeak) as
// one JSON string in the charging namespace of their settings row. It goes with
// the account, stays through a history reset, and is in the settings export.
const keyChargingOffpeak = "charging.offpeak_schedule"

// GetOffpeak returns the saved schedule; ok is false when there is none (or the
// stored text no longer parses, which reads as none rather than failing a page).
func GetOffpeak(ctx context.Context, s *Store) (offpeak.Schedule, bool, error) {
	if s == nil {
		return offpeak.Schedule{Windows: []offpeak.Window{}}, false, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return offpeak.Schedule{}, false, err
	}
	raw := all[keyChargingOffpeak]
	if raw == "" {
		return offpeak.Schedule{Windows: []offpeak.Window{}}, false, nil
	}
	var sch offpeak.Schedule
	if json.Unmarshal([]byte(raw), &sch) != nil {
		return offpeak.Schedule{Windows: []offpeak.Window{}}, false, nil
	}
	if norm, e := offpeak.Normalize(sch); e == nil && len(norm.Windows) > 0 {
		return norm, true, nil
	}
	return offpeak.Schedule{Windows: []offpeak.Window{}}, false, nil
}

// SetOffpeak saves an already validated schedule (offpeak.Normalize); a schedule
// with no windows clears it.
func SetOffpeak(ctx context.Context, s *Store, sch offpeak.Schedule) error {
	if s == nil {
		return nil
	}
	if len(sch.Windows) == 0 {
		return s.Delete(ctx, keyChargingOffpeak)
	}
	b, err := json.Marshal(sch)
	if err != nil {
		return err
	}
	return s.Set(ctx, keyChargingOffpeak, string(b))
}
