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

    let powerKw: Double
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
        let power: Double = s.flatMap { $0.powerKw > 0 ? $0.powerKw : nil } ?? summary.chargePowerKw ?? 0
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
            cost: s.flatMap(cost(of:)),
            place: charge?.placeLabel,
            haveLiveData: live
        )
    }

    private static func cost(of s: LiveSession) -> Cost? {
        // Any price Rivian sent counts as billed, "0.00" included (the web does the same).
        if let billed = Double(s.currentPrice.trimmingCharacters(in: .whitespaces)), billed >= 0 {
            return .billed(amount: billed, currency: s.currentCurrency)
        }
        if let est = s.estimatedCost, est > 0 {
            return .estimate(amount: est, currency: s.estimatedCurrency ?? "")
        }
        return s.isFreeSession ? .free : nil
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

enum NowFormat {
    /// "42 min" or "1 h 12 min".
    static func duration(_ seconds: TimeInterval) -> String {
        let m = max(0, Int((seconds / 60).rounded()))
        return m >= 60 ? "\(m / 60) h \(m % 60) min" : "\(m) min"
    }

    /// An amount in a currency code; plain digits when the code isn't a usable one.
    static func money(_ amount: Double, currency: String) -> String {
        guard currency.count == 3 else { return String(format: "%.2f", amount) }
        return amount.formatted(.currency(code: currency.uppercased()))
    }

    static func costText(_ c: ChargingDetail.Cost) -> String {
        switch c {
        case .billed(let a, let cur): return money(a, currency: cur)
        case .estimate(let a, let cur): return "~\(money(a, currency: cur)) (estimate)"
        case .free: return "Free"
        }
    }
}
