import Foundation

/// What the Now card says about a charge in progress and about the current or last drive: the same figures as the web's
/// Now page, worked out here without any view code so they can be tested. Costs are always marked as estimates when they
/// are ours; nothing here is a place or a coordinate.
struct ChargingDetail: Equatable {
    enum Cost: Equatable {
        /// What Rivian reports so far for this session.
        case billed(amount: Double, currency: String)
        /// Our estimate from the home rate.
        case estimate(amount: Double, currency: String)
        case free
    }

    /// Nil when neither the session nor the state has a reading: the card shows a dash, never a made-up 0.
    let powerKw: Double?
    let socPct: Double?
    let targetPct: Double?
    /// Percentage points left to the charge limit, when there is one ahead.
    let toTargetPct: Double?
    let rateKmPerHour: Double?
    let rangeAddedKm: Double?
    let energyKwh: Double?
    let elapsed: TimeInterval?
    let remaining: TimeInterval?
    let cost: Cost?
    /// Home, Away or Fast, from the charge row (never a coordinate).
    let place: String?
    /// Rivian's live feed gave nothing yet (typical of a home charge): the card then shows only the basics.
    let haveLiveData: Bool

    /// Nil unless the truck is charging (the Now card shows nothing for a charge that isn't happening).
    static func make(summary: NowSummary, session: LiveSession?, charge: ChargeBrief?) -> ChargingDetail? {
        guard summary.stateLabel == "Charging" else { return nil }
        let s = session
        let power: Double? = s.flatMap { $0.powerKw > 0 ? $0.powerKw : nil } ?? summary.chargePowerKw.flatMap { $0 > 0 ? $0 : nil }
        let soc: Double? = s.flatMap { $0.socPct > 0 ? $0.socPct : nil } ?? summary.socPct
        let target = summary.chargeTargetPct
        let toTarget: Double? = {
            guard let soc, let target, target > soc else { return nil }
            return target - soc
        }()
        let live = s.map { $0.active || $0.powerKw > 0 || $0.totalChargedEnergyKwh > 0 } ?? false
        return ChargingDetail(
            powerKw: power,
            socPct: soc,
            targetPct: target,
            toTargetPct: toTarget,
            rateKmPerHour: s.flatMap { $0.kilometersChargedPerHour > 0 ? $0.kilometersChargedPerHour : nil },
            rangeAddedKm: s.flatMap { $0.rangeAddedKm > 0 ? $0.rangeAddedKm : nil },
            energyKwh: s.flatMap { $0.totalChargedEnergyKwh > 0 ? $0.totalChargedEnergyKwh : nil },
            elapsed: s.flatMap { $0.timeElapsedSeconds > 0 ? $0.timeElapsedSeconds : nil },
            remaining: s.flatMap { $0.timeRemainingSeconds > 0 ? $0.timeRemainingSeconds : nil },
            cost: cost(of: s, summary: summary, charge: charge),
            place: charge?.placeLabel,
            haveLiveData: live
        )
    }

    /// The charge's cost so far, in order of trust. (1) What Rivian bills for the session: its own figure. (2) The server's open charge row,
    /// priced from the energy that charge has recorded: the figure the web's Now card is built on. (3) The live session's estimate, only
    /// while the energy it was made from agrees with the battery's own gain so far: it was once the previous session's total ($9.39 "so far"
    /// a minute into a charge that had added 0.2 kWh). Otherwise nothing: the row stays out until there is a real figure.
    static func cost(of s: LiveSession?, summary: NowSummary, charge: ChargeBrief?) -> Cost? {
        // Any price Rivian sent counts as billed, "0.00" included (the web does the same).
        if let s, let billed = Double(s.currentPrice.trimmingCharacters(in: .whitespaces)), billed >= 0 {
            return .billed(amount: billed, currency: s.currentCurrency)
        }
        if let open = openCharge(charge, summary: summary) {
            if open.cost > 0, let cur = usableCurrency(open.currency) { return .estimate(amount: open.cost, currency: cur) }
            if let est = open.estimatedCost, est > 0, let cur = usableCurrency(open.estimatedCurrency) { return .estimate(amount: est, currency: cur) }
        }
        guard let s else { return nil }
        // An estimate with no currency would read as a bare number, so it is left out.
        if let est = s.estimatedCost, est > 0, let cur = usableCurrency(s.estimatedCurrency), sessionEnergyAgrees(s, summary: summary) {
            return .estimate(amount: est, currency: cur)
        }
        return s.isFreeSession ? .free : nil
    }

    private static func usableCurrency(_ c: String?) -> String? {
        guard let c, c.count == 3 else { return nil }
        return c
    }

    /// The server's charge row for the charge in progress: the one that began when `/now` says it did (within five minutes), or, with no start
    /// time from `/now`, one that began in the last 12 hours and was still being written to.
    private static func openCharge(_ charge: ChargeBrief?, summary: NowSummary) -> ChargeBrief? {
        guard let charge, let started = charge.startedAt else { return nil }
        if let begun = summary.chargeStartedAt { return abs(started.timeIntervalSince(begun)) <= 5 * 60 ? charge : nil }
        guard let age = summary.ageSec, age.isFinite else { return nil }
        let clock = (summary.lastSeen ?? .now).addingTimeInterval(age)
        return clock.timeIntervalSince(started) <= 12 * 3600 && (charge.endedAt.map { clock.timeIntervalSince($0) <= 30 * 60 } ?? true) ? charge : nil
    }

    /// The live session's energy against the battery's gain so far (percent gained x the pack). Unknown gain: no agreement, so no estimate.
    private static func sessionEnergyAgrees(_ s: LiveSession, summary: NowSummary) -> Bool {
        guard let added = summary.chargeAddedPct, let pack = summary.packKwh, pack > 0 else { return false }
        let gain = added / 100 * pack
        return abs(s.totalChargedEnergyKwh - gain) <= max(1.5, 0.35 * gain)
    }
}

/// The drive in progress, or the last one.
struct DriveDetail: Equatable {
    enum Kind: Equatable {
        case now
        case last(endedAt: Date?)
    }

    let kind: Kind
    let distanceMi: Double
    let elapsed: TimeInterval?
    let avgSpeedMph: Double?
    let maxSpeedMph: Double?
    let energyKwh: Double?
    let miPerKwh: Double?
    let estimatedCost: Double?
    let currency: String?

    /// While in gear, the live drive; otherwise the newest recorded one. Nil when there is nothing to say.
    static func make(summary: NowSummary, live: LiveDrive?, last: DriveBrief?) -> DriveDetail? {
        if summary.stateLabel == "Driving", let d = live, d.distanceMi > 0 || d.elapsedSec > 0 {
            return DriveDetail(
                kind: .now, distanceMi: d.distanceMi, elapsed: d.elapsedSec > 0 ? d.elapsedSec : nil,
                avgSpeedMph: d.avgSpeedMph > 0 ? d.avgSpeedMph : nil, maxSpeedMph: d.maxSpeedMph > 0 ? d.maxSpeedMph : nil,
                energyKwh: d.energyUsedKwh > 0 ? d.energyUsedKwh : nil, miPerKwh: d.miPerKwh > 0 ? d.miPerKwh : nil,
                estimatedCost: nil, currency: nil)
        }
        guard let l = last, l.distanceMi > 0 else { return nil }
        let ended = l.endedAt.flatMap { $0.timeIntervalSince1970 > 946_684_800 ? $0 : nil }
        return DriveDetail(
            kind: .last(endedAt: ended), distanceMi: l.distanceMi,
            elapsed: {
                guard let s = l.startedAt, let e = ended else { return nil }
                return e.timeIntervalSince(s)
            }(),
            avgSpeedMph: l.avgSpeedMph > 0 ? l.avgSpeedMph : nil, maxSpeedMph: nil,
            energyKwh: l.energyUsedKwh > 0 ? l.energyUsedKwh : nil, miPerKwh: l.miPerKwh,
            estimatedCost: l.estimatedCost.flatMap { $0 > 0 ? $0 : nil }, currency: l.estimatedCurrency)
    }
}

/// Where the charge in progress is (Home, Away or Fast), from the latest charge's ledger row, but only when that row
/// is this session: while charging, the newest row can still be the previous, finished session, and saying "at home"
/// for a charge that began elsewhere would be wrong. Without a match there is no place.
enum ChargingPlace {
    /// How far apart the two start times may be and still be the same session.
    static let tolerance: TimeInterval = 15 * 60

    static func label(summary: NowSummary, charge: ChargeBrief?) -> String? {
        guard summary.kind == .charging, let started = summary.chargeStartedAt,
              let charge, let rowStart = charge.startedAt,
              abs(rowStart.timeIntervalSince(started)) <= tolerance else { return nil }
        return charge.placeLabel
    }
}

enum NowFormat {
    /// How long ago, in the web's words: "just now", "5 min ago", "3 h ago", "2 d ago".
    static func ago(_ seconds: TimeInterval) -> String {
        guard seconds.isFinite else { return "a while ago" }
        let m = Int(max(0, seconds) / 60)
        if m < 1 { return "just now" }
        if m < 60 { return "\(m) min ago" }
        let h = m / 60
        if h < 48 { return "\(h) h ago" }
        return "\(h / 24) d ago"
    }

    /// "42 min" or "1 h 12 min".
    static func duration(_ seconds: TimeInterval) -> String {
        let m = max(0, Int((seconds / 60).rounded()))
        return m >= 60 ? "\(m / 60) h \(m % 60) min" : "\(m) min"
    }

    /// An amount in a currency code; plain digits when the code isn't a usable one.
    static func money(_ amount: Double, currency: String) -> String {
        NumberText.money(amount, currency)
    }

    static func costText(_ c: ChargingDetail.Cost) -> String {
        switch c {
        case .billed(let a, let cur): return money(a, currency: cur)
        case .estimate(let a, let cur): return "~\(money(a, currency: cur)) (estimate)"
        case .free: return "Free"
        }
    }
}

// MARK: What VoiceOver says and how numbers read (kept out of the views so they can be tested)

extension NowFormat {
    /// One decimal, in the phone's number format.
    static func oneDecimal(_ v: Double) -> String { NumberText.fixed(v, 1) }
    static func twoDecimals(_ v: Double) -> String { NumberText.fixed(v, 2) }

    /// Duration said in words: "1 hour 12 minutes".
    static func spokenDuration(_ seconds: TimeInterval) -> String {
        let m = max(0, Int((seconds / 60).rounded()))
        let h = m / 60, r = m % 60
        func unit(_ n: Int, _ one: String) -> String { "\(n) \(one)\(n == 1 ? "" : "s")" }
        if h == 0 { return unit(r, "minute") }
        return r == 0 ? unit(h, "hour") : "\(unit(h, "hour")) \(unit(r, "minute"))"
    }

    static func spokenCost(_ c: ChargingDetail.Cost) -> String {
        switch c {
        case .billed(let a, let cur): return money(a, currency: cur)
        case .estimate(let a, let cur): return "about \(money(a, currency: cur)), an estimate"
        case .free: return "free"
        }
    }

    static var usesMetric: Bool {
        let system = Locale.current.measurementSystem
        return system != .us && system != .uk
    }

    /// A distance recorded in miles, shown (or said, with `spoken`) the way the phone's units say.
    static func distance(mi: Double, spoken: Bool = false) -> String {
        let metric = usesMetric
        let unit = spoken ? (metric ? "kilometres" : "miles") : (metric ? "km" : "mi")
        return "\(NumberText.fixed(metric ? mi * 1.609344 : mi, 1)) \(unit)"
    }

    static func speed(mph: Double, spoken: Bool = false) -> String {
        let metric = usesMetric
        let unit = spoken ? (metric ? "kilometres per hour" : "miles per hour") : (metric ? "km/h" : "mph")
        return "\(NumberText.fixed(metric ? mph * 1.609344 : mph, 0)) \(unit)"
    }

    /// mi/kWh, or km/kWh on a metric phone.
    static func efficiency(miPerKwh: Double, spoken: Bool = false) -> String {
        let value = twoDecimals(usesMetric ? miPerKwh * 1.609344 : miPerKwh)
        switch (usesMetric, spoken) {
        case (true, true): return "\(value) kilometres per kilowatt hour"
        case (true, false): return "\(value) km/kWh"
        case (false, true): return "\(value) miles per kilowatt hour"
        case (false, false): return "\(value) mi/kWh"
        }
    }

    /// An amount only when its currency is a real code: a bare "3.10" with no symbol says nothing about money.
    static func moneyIfKnown(_ amount: Double, currency: String?) -> String? {
        guard let currency, currency.count == 3 else { return nil }
        return money(amount, currency: currency)
    }
}

extension ChargingDetail {
    /// The card read as sentences, so VoiceOver doesn't spell out "kW", "~" and "%".
    var spoken: String {
        let where_: String = {
            switch place {
            case "Home": return " at home"
            case "Away": return " away from home"
            case "Fast": return ", fast charger"
            default: return ""
            }
        }()
        var parts = ["Charging session" + where_ + "."]
        if let powerKw { parts.append("\(NowFormat.oneDecimal(powerKw)) kilowatts.") }
        if let socPct, let targetPct, targetPct > socPct {
            parts.append("\(Int(socPct.rounded())) percent, charge limit \(Int(targetPct.rounded())) percent.")
        } else if let socPct {
            parts.append("\(Int(socPct.rounded())) percent.")
        }
        guard haveLiveData else { return (parts + ["Waiting for the first reading from the charger."]).joined(separator: " ") }
        if let remaining { parts.append("About \(NowFormat.spokenDuration(remaining)) left.") }
        if let elapsed { parts.append("Elapsed \(NowFormat.spokenDuration(elapsed)).") }
        if let energyKwh { parts.append("\(NowFormat.oneDecimal(energyKwh)) kilowatt hours added.") }
        if let cost { parts.append("Cost so far, \(NowFormat.spokenCost(cost)).") }
        return parts.joined(separator: " ")
    }
}

extension DriveDetail {
    func spoken(title: String) -> String {
        var parts = ["\(title)."]
        parts.append("\(NowFormat.distance(mi: distanceMi, spoken: true)).")
        if let elapsed { parts.append("\(NowFormat.spokenDuration(elapsed)).") }
        if let avgSpeedMph { parts.append("Average \(NowFormat.speed(mph: avgSpeedMph, spoken: true)).") }
        if let energyKwh { parts.append("\(NowFormat.oneDecimal(energyKwh)) kilowatt hours used.") }
        if let miPerKwh { parts.append("\(NowFormat.efficiency(miPerKwh: miPerKwh, spoken: true)).") }
        if let estimatedCost, let m = NowFormat.moneyIfKnown(estimatedCost, currency: currency) { parts.append("Cost about \(m), an estimate.") }
        return parts.joined(separator: " ")
    }
}

