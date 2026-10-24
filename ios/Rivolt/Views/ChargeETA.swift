import Foundation

/// The time left in a charge: the truck's own figure when it is trustworthy, else ours. The same rule as
/// internal/rivian/charge_eta.go and web/src/lib/chargeEta.ts; all three pass internal/rivian/testdata/charge_eta_vectors.json
/// (copied to RivoltTests/Fixtures; a Go test keeps the copy identical). Change a vector before changing any of them.
enum ChargeETA {
    enum Source: Equatable {
        /// Rivian's own figure: shown plainly, as the Rivian app shows it.
        case truck
        /// Ours, a straight line at the current power: shown as an estimate.
        case estimate
    }

    struct Result: Equatable {
        let minutes: Int
        let source: Source
    }

    /// A longer figure is stale or nonsense (a day).
    static let maxMinutes: Double = 24 * 60
    /// The truck's figure is dropped when under a quarter or over four times ours (a taper makes it longer than a line).
    static let guardLow = 0.25
    static let guardHigh = 4.0
    private static let minKw = 0.1

    /// Our straight-line estimate in whole minutes (at least 1), or 0 when anything is unknown or at the limit.
    static func ownMinutes(socPct: Double, limitPct: Double, packKwh: Double, powerKw: Double) -> Double {
        guard limitPct > socPct, packKwh > 0, powerKw > minKw else { return 0 }
        let kwh = (limitPct - socPct) / 100 * packKwh
        return max(1, (kwh / powerKw * 60).rounded(.up))
    }

    /// What to show. `truckMins` is the truck's figure (0 when none), `validity` its charging_time_estimation_validity,
    /// `limitPct` the charge limit (0 when unknown), `oursMins` our estimate (0 when none). Nil when there is nothing to show.
    static func pick(truckMins: Double, validity: String?, socPct: Double, limitPct: Double, oursMins: Double) -> (minutes: Double, source: Source)? {
        var ok = truckMins > 0 && truckMins <= maxMinutes && truckMins.isFinite
            && (validity ?? "").trimmingCharacters(in: .whitespaces).lowercased() != "invalid"
        if ok && limitPct > 0 && socPct >= limitPct { ok = false }
        if ok && oursMins > 0 && (truckMins < guardLow * oursMins || truckMins > guardHigh * oursMins) { ok = false }
        if ok { return (truckMins, .truck) }
        if oursMins > 0 { return (oursMins, .estimate) }
        return nil
    }
}
