package rivian

import (
	"math"
	"strings"
)

// The time left in a charge, shown on Now (web and iPhone) and in the charging Live Activity.
//
// The truck's own figure (vehicleState timeToEndOfCharge, or an R2's Parallax charging.session.time_estimation) is
// preferred: it follows the taper, which a straight line from the current power cannot. Ours stays as the fallback
// and as the check on the truck's figure. The rule lives here and is mirrored in web/src/lib/chargeEta.ts and
// ios/Rivolt/Views/ChargeETA.swift; all three pass internal/rivian/testdata/charge_eta_vectors.json. Change a
// vector before changing any of them.

const (
	// ETASourceTruck: the minutes are the truck's own (shown without "est.").
	ETASourceTruck = "truck"
	// ETASourceEstimate: the minutes are ours (shown with "est.").
	ETASourceEstimate = "estimate"

	// etaMaxMins: a longer figure is a stale or nonsense value (a day).
	etaMaxMins = 24 * 60
	// The truck's figure is dropped when it is under a quarter or over four times ours: a taper makes the truck's
	// longer than a straight line, so the band only catches stale or wrong-target values.
	etaGuardLow  = 0.25
	etaGuardHigh = 4.0
	// etaMinKW: below this the charge is not delivering power and a straight-line figure is meaningless.
	etaMinKW = 0.1
)

// OwnChargeETAMins is our straight-line estimate: the energy still to add up to the charge limit, at the current
// power, in whole minutes (at least 1). 0 when anything is unknown or the charge is already at the limit.
func OwnChargeETAMins(socPct, limitPct, packKWh, powerKW float64) float64 {
	if limitPct <= socPct || packKWh <= 0 || powerKW <= etaMinKW {
		return 0
	}
	kwh := (limitPct - socPct) / 100 * packKWh
	return math.Max(1, math.Ceil(kwh/powerKW*60))
}

// PickChargeETA chooses what to show. truckMins is the truck's figure (0 when it sent none), validity its
// charging_time_estimation_validity ("invalid" hides it), limitPct the charge limit (0 when unknown) and oursMins
// OwnChargeETAMins (0 when none). It returns the minutes and their source, or (0, "") when there is nothing to show.
func PickChargeETA(truckMins float64, validity string, socPct, limitPct, oursMins float64) (float64, string) {
	ok := truckMins > 0 && truckMins <= etaMaxMins && !math.IsNaN(truckMins) && !strings.EqualFold(strings.TrimSpace(validity), "invalid")
	if ok && limitPct > 0 && socPct >= limitPct {
		ok = false // already at the limit: the truck's figure is the tail of the last charge
	}
	if ok && oursMins > 0 && (truckMins < etaGuardLow*oursMins || truckMins > etaGuardHigh*oursMins) {
		ok = false
	}
	switch {
	case ok:
		return truckMins, ETASourceTruck
	case oursMins > 0:
		return oursMins, ETASourceEstimate
	default:
		return 0, ""
	}
}

// ChargeETAFromState is the rule applied to a vehicle's latest State (the Live Activity's source).
func ChargeETAFromState(st *State) (float64, string) {
	if st == nil {
		return 0, ""
	}
	ours := OwnChargeETAMins(st.BatteryLevelPct, st.ChargeTargetPct, st.BatteryCapacityKWh, st.ChargerPowerKW)
	return PickChargeETA(st.TimeToEndMins, st.ChargingTimeEstimationValidity, st.BatteryLevelPct, st.ChargeTargetPct, ours)
}
