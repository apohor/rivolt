package stretched

import (
	"context"
	"database/sql"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// NewPriceFn prices and places the new rows a split creates, with the recorder's own function (rivian.PriceUnbilled):
// an AC charge at home gets the user's home rate (cost_source home_rate) and a place, an AC charge away from home is placed
// "away" and left unpriced. A DC fast charge is left unpriced and unplaced here: it needs the chargers archive and network
// detection, which `rivolt reprice --detect` has, with its own dry run. No call reaches Rivian: the lookups are settings reads.
func NewPriceFn(ctx context.Context, pool *sql.DB) PriceFn {
	sf := settings.NewFactory(pool)
	type perUser struct{ s rivian.PriceSettings }
	cache := map[uuid.UUID]*perUser{}
	return func(uid uuid.UUID, c *charges.Charge) {
		if rivian.IsDCFCSession(c.MaxPowerKW, c.EnergyAddedKWh, c.ActiveSeconds, c.EndedAt.Sub(c.StartedAt)) {
			return
		}
		pu := cache[uid]
		if pu == nil {
			pu = &perUser{}
			if ss := sf.For(uid); ss != nil {
				pu.s.Home = func() (float64, string) {
					cfg, err := settings.GetChargingConfig(ctx, ss)
					if err != nil {
						return 0, ""
					}
					return cfg.HomePricePerKWh, cfg.HomeCurrency
				}
				pu.s.HomeLoc = func() (float64, float64, bool) {
					h, err := settings.GetHomeLocation(ctx, ss)
					if err != nil || !h.Set {
						return 0, 0, false
					}
					return h.Latitude, h.Longitude, true
				}
			}
			cache[uid] = pu
		}
		if c.Cost == 0 && c.EnergyAddedKWh > 0 {
			rivian.PriceUnbilled(c, pu.s, &rivian.NetworkMemo{})
		}
		c.Place = rivian.PlaceOf(c.Lat, c.Lon, pu.s.HomeLoc)
	}
}
