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
    /// What the truck is doing, as the server (or `/state`) says; the label above is this in words.
    let kind: NowSnapshot.State
    /// The newest frame says the truck is asleep, whatever `kind` reads (a plugged-in truck can sleep).
    let sleeping: Bool
    /// The time left in a charge in progress: the truck's own figure when it is valid and plausible, else our estimate
    /// (ChargeETA). Nil when not charging or when neither exists.
    let chargeEta: ChargeETA.Result?
    /// How old the newest frame is, at the `clock` the summary was made with.
    let ageSec: TimeInterval?
    /// Percentage points since the current state began (parked, plugged or asleep only).
    let socChangeSinceState: Double?
    /// When the charge in progress began, from `/now`; nil when there is none or the server doesn't say.
    let chargeStartedAt: Date?
    /// From `/now`: failed sleeps in the last 6 hours and the lowest tire pressure.
    let failedSleeps6h: Int?
    let tireMinBar: Double?
    /// Set by the screen when the numbers are older than its last read of the server, so "Offline" would be our cache
    /// speaking, not the truck: the presence reads stale until the server has been asked again (see `NowFreshness`).
    var holdOffline = false
    /// Cabin temperature, preconditioning, pet mode and climate hold as the newest frame reports them.
    let comfort: NowComfort?
    let update: OtaStep?

    /// The comfort line to show: only while the frame is live. A stale, offline, paused or to-be-reconnected truck shows
    /// none, so an old "Pet mode on" is never read as current.
    /// The software update, only while the truck says it is installing and the frame is live: it explains why the truck may stop answering.
    var liveInstall: OtaStep? {
        presence == .live && freshness != .needsReconnect && freshness != .paused && update?.key == "installing" ? update : nil
    }

    var liveComfort: NowComfort? {
        presence == .live && freshness != .needsReconnect && freshness != .paused ? comfort : nil
    }

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
        var comfort: NowComfort?
        var update: OtaStep?
        var timeToEndMins: Double?
        var timeToEndValidity: String?

        init(socPct: Double?, rangeKm: Double?, chargeTargetPct: Double?, gear: String?, powerState: String?,
             chargerState: String?, chargerPowerKw: Double?, at: Date?, packKwh: Double? = nil, speedKph: Double? = nil,
             comfort: NowComfort? = nil, update: OtaStep? = nil, timeToEndMins: Double? = nil, timeToEndValidity: String? = nil) {
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
            self.comfort = comfort
            self.update = update
            self.timeToEndMins = timeToEndMins
            self.timeToEndValidity = timeToEndValidity
        }

        init(_ s: VehicleState) {
            self.init(socPct: s.batteryLevelPct, rangeKm: s.distanceToEmpty, chargeTargetPct: s.chargeTargetPct,
                      gear: s.gear, powerState: s.powerState, chargerState: s.chargerState,
                      chargerPowerKw: s.chargerPowerKw, at: s.at, packKwh: s.batteryCapacityKwh, speedKph: s.speedKph,
                      comfort: NowComfort(cabinTempC: s.cabinTempC, preconditioning: s.cabinPreconditioningStatus, petMode: s.petModeStatus, cabinHold: s.cabinHoldStatus),
                      update: OtaStep(s), timeToEndMins: s.timeToEndMins, timeToEndValidity: s.chargingTimeEstimationValidity)
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
        self.kind = kind
        sleeping = now?.asleep ?? (input?.powerState == "sleep")
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
        ageSec = lastSeen.map { max(0, clock.timeIntervalSince($0)) }
        if let start = now?.socAtStateStart, let soc = input?.socPct, [.parked, .plugged, .asleep].contains(kind) {
            socChangeSinceState = soc - start
        } else {
            socChangeSinceState = nil
        }
        if kind == .charging, let soc = input?.socPct {
            let limit = chargeTargetPct ?? 0
            let ours = ChargeETA.ownMinutes(socPct: soc, limitPct: limit, packKwh: input?.packKwh ?? 0, powerKw: chargePowerKw ?? 0)
            chargeEta = ChargeETA.pick(truckMins: input?.timeToEndMins ?? 0, validity: input?.timeToEndValidity, socPct: soc, limitPct: limit, oursMins: ours)
                .map { ChargeETA.Result(minutes: max(1, Int($0.minutes.rounded())), source: $0.source) }
        } else {
            chargeEta = nil
        }
        chargeStartedAt = now?.charge?.startedAt
        failedSleeps6h = now?.failedSleeps6h
        tireMinBar = now?.tirePressureMinBar
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
        comfort = input?.comfort
        update = input?.update
        speedKph = (kind == .driving) ? input?.speedKph.flatMap { $0 >= 0 ? $0 : nil } : nil
    }

    /// How current the newest frame is, by the web's limits (Now's hero): stale past the first, offline past the second.
    /// Separate from `freshness`, which the widgets use with their own, looser cadence.
    enum Presence: Equatable { case live, stale, offline }

    /// The ages, in seconds, past which the newest frame reads stale and then offline, for what the truck is doing.
    var limits: (stale: TimeInterval, offline: TimeInterval) {
        switch kind == .plugged ? (sleeping ? NowSnapshot.State.asleep : .parked) : kind {
        case .driving: return (30, 180)
        case .charging: return (120, 600)
        case .asleep: return (1800, 2700)
        default: return (120, 900)
        }
    }

    var presence: Presence {
        guard let age = ageSec else { return holdOffline ? .stale : .offline }
        let limits = self.limits
        if age > limits.offline { return holdOffline ? .stale : .offline }
        return age > limits.stale ? .stale : .live
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
