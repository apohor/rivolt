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
    /// The pack size, when the truck reports it.
    let packKwh: Double?
    /// Speed in km/h, when the truck reports one.
    let speedKph: Double?

    /// The few fields the card works from. The widget builds this directly from
    /// the fields it decodes (it never decodes a location), the app from a full
    /// `VehicleState`.
    struct Input: Equatable {
        var socPct: Double?
        var rangeKm: Double?
        var chargeTargetPct: Double?
        var gear: String?
        var powerState: String?
        var chargerState: String?
        var chargerPowerKw: Double?
        var at: Date?
        var packKwh: Double?
        var speedKph: Double?

        init(socPct: Double?, rangeKm: Double?, chargeTargetPct: Double?, gear: String?, powerState: String?,
             chargerState: String?, chargerPowerKw: Double?, at: Date?, packKwh: Double? = nil, speedKph: Double? = nil) {
            self.socPct = socPct
            self.rangeKm = rangeKm
            self.chargeTargetPct = chargeTargetPct
            self.gear = gear
            self.powerState = powerState
            self.chargerState = chargerState
            self.chargerPowerKw = chargerPowerKw
            self.at = at
            self.packKwh = packKwh
            self.speedKph = speedKph
        }

        init(_ s: VehicleState) {
            self.init(socPct: s.batteryLevelPct, rangeKm: s.distanceToEmpty, chargeTargetPct: s.chargeTargetPct,
                      gear: s.gear, powerState: s.powerState, chargerState: s.chargerState,
                      chargerPowerKw: s.chargerPowerKw, at: s.at, packKwh: s.batteryCapacityKwh, speedKph: s.speedKph)
        }
    }

    init(state: VehicleState?, now: NowSnapshot?, clock: Date) {
        self.init(input: state.map(Input.init), now: now, clock: clock)
    }

    init(input: Input?, now: NowSnapshot?, clock: Date) {
        socPct = input?.socPct
        rangeKm = input?.rangeKm
        chargeTargetPct = input.flatMap { $0.chargeTargetPct.flatMap { $0 > 0 ? $0 : nil } }

        let kind = now?.state ?? NowSummary.inferState(from: input)
        stateLabel = NowSummary.label(kind, asleep: now?.asleep ?? false)
        since = now?.stateSince
        chargePowerKw = (kind == .charging) ? input?.chargerPowerKw.flatMap { $0 > 0 ? $0 : nil } : nil
        if let charge = now?.charge, let start = charge.socStart, let soc = input?.socPct {
            chargeAddedPct = max(0, soc - start)
        } else {
            chargeAddedPct = nil
        }
        chargePeakKw = now?.charge?.peakKw

        lastSeen = [now?.at, input?.at].compactMap { $0 }.max()
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
        packKwh = input?.packKwh.flatMap { $0 > 0 ? $0 : nil }
        speedKph = (kind == .driving) ? input?.speedKph.flatMap { $0 >= 0 ? $0 : nil } : nil
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
        inferState(from: state.map(Input.init))
    }

    static func inferState(from state: Input?) -> NowSnapshot.State {
        guard let state else { return .unknown }
        if state.chargerState == "charging_active" { return .charging }
        if let gear = state.gear, ["D", "R"].contains(gear) { return .driving }
        if state.powerState == "sleep" { return .asleep }
        if let c = state.chargerState, !c.isEmpty, c != "charger_disconnected" { return .plugged }
        return .parked
    }
}
