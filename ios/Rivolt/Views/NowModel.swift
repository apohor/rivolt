import Foundation

/// What the Now card says, worked out from `/state` and `/now` without any
/// view code, so it can be unit-tested. Read-only: nothing here acts on the
/// truck.
struct NowSummary: Equatable {
    enum Freshness: Equatable {
        /// Within the expected cadence for the truck's state.
        case fresh
        /// Older than that: the numbers may be out of date.
        case stale
        /// Rivolt can't reach the truck until the Rivian account is
        /// reconnected (on the web).
        case needsReconnect
        /// Recording is paused for this truck.
        case paused
        case unknown
    }

    let socPct: Double?
    let rangeKm: Double?
    let chargeTargetPct: Double?
    let stateLabel: String
    let since: Date?
    let chargePowerKw: Double?
    /// Percentage points added since the charge started.
    let chargeAddedPct: Double?
    let chargePeakKw: Double?
    let lastSeen: Date?
    let freshness: Freshness
    let outsideTempC: Double?

    init(state: VehicleState?, now: NowSnapshot?, clock: Date) {
        socPct = state?.batteryLevelPct
        rangeKm = state?.distanceToEmpty
        chargeTargetPct = state.flatMap { $0.chargeTargetPct.flatMap { $0 > 0 ? $0 : nil } }

        let kind = now?.state ?? NowSummary.inferState(from: state)
        stateLabel = NowSummary.label(kind, asleep: now?.asleep ?? false)
        since = now?.stateSince
        chargePowerKw = (kind == .charging) ? state?.chargerPowerKw.flatMap { $0 > 0 ? $0 : nil } : nil
        if let charge = now?.charge, let start = charge.socStart, let soc = state?.batteryLevelPct {
            chargeAddedPct = max(0, soc - start)
        } else {
            chargeAddedPct = nil
        }
        chargePeakKw = now?.charge?.peakKw

        lastSeen = [now?.at, state?.at].compactMap { $0 }.max()
        switch now?.connection {
        case .reauth?: freshness = .needsReconnect
        case .paused?: freshness = .paused
        default:
            if let seen = lastSeen {
                freshness = clock.timeIntervalSince(seen) <= NowSummary.cadence(kind) ? .fresh : .stale
            } else {
                freshness = .unknown
            }
        }
        outsideTempC = now?.ambient?.tempC
    }

    /// How old the newest data may be before the card says it may be out of
    /// date. A moving or charging truck reports every few seconds; a parked
    /// one every few minutes; an asleep one rarely.
    static func cadence(_ state: NowSnapshot.State) -> TimeInterval {
        switch state {
        case .driving, .charging: return 10 * 60
        case .plugged, .parked: return 60 * 60
        case .asleep: return 12 * 60 * 60
        case .unknown: return 60 * 60
        }
    }

    static func label(_ state: NowSnapshot.State, asleep: Bool) -> String {
        switch state {
        case .driving: return "Driving"
        case .charging: return "Charging"
        case .plugged: return asleep ? "Plugged in, asleep" : "Plugged in"
        case .parked: return "Parked"
        case .asleep: return "Asleep"
        case .unknown: return "Status unknown"
        }
    }

    /// Without `/now` (an older server), a rough state from `/state` alone.
    static func inferState(from state: VehicleState?) -> NowSnapshot.State {
        guard let state else { return .unknown }
        if state.chargerState == "charging_active" { return .charging }
        if let gear = state.gear, ["D", "R"].contains(gear) { return .driving }
        if state.powerState == "sleep" { return .asleep }
        if let c = state.chargerState, !c.isEmpty, c != "charger_disconnected" { return .plugged }
        return .parked
    }
}
