import Foundation

/// The numbers on a charge's "Session" card, worked out as the web's charge page does: what needs the sample stream
/// (the time to peak, the time spent charging, the limit in force, the temperatures) from the samples, the rest from the
/// charge's own row, so the card still says something when the samples can't be read.
struct ChargeInsights: Equatable {
    struct Tier: Equatable {
        let label: String
        let hint: String
    }

    var tier: Tier
    var timeToPeakSec: Double
    var activeSec: Double
    /// The share of the session spent above 0 kW.
    var activePct: Double
    var socGainedPct: Double
    var socPerHour: Double
    var limitPct: Int?
    /// "~0.130 USD/kWh": priced per kWh, with a "~" when it is an estimate.
    var costPerKwh: String?
    var outsideTempC: Double?
    var insideTempC: Double?

    /// The charger class by its peak power, in the web's words.
    static func tier(maxKw: Double) -> Tier {
        if maxKw <= 0 { return Tier(label: "—", hint: "no power data") }
        if maxKw <= 2 { return Tier(label: "L1 (120 V)", hint: "wall outlet") }
        if maxKw <= 12 { return Tier(label: "L2", hint: "7–12 kW range") }
        if maxKw <= 25 { return Tier(label: "L2 high power", hint: "12–25 kW range") }
        if maxKw <= 60 { return Tier(label: "DC slow", hint: "~50 kW DCFC") }
        if maxKw <= 150 { return Tier(label: "DC standard", hint: "100–150 kW DCFC") }
        if maxKw <= 250 { return Tier(label: "DC fast", hint: "150–250 kW DCFC") }
        return Tier(label: "DC ultra", hint: "350 kW+ DCFC")
    }

    /// A step between two samples counts as charging time up to this long, so a dropout isn't counted as charging.
    static let activeStepCap: TimeInterval = 300

    static func compute(_ charge: ActivityCharge, samples: [ChargeSample]) -> ChargeInsights {
        let window = samples
            .filter { $0.at >= charge.startedAt && $0.at <= charge.endedAt.addingTimeInterval(60) }
            .sorted { $0.at < $1.at }
        let totalSec = max(0, charge.endedAt.timeIntervalSince(charge.startedAt))

        var active = 0.0
        for i in window.indices.dropFirst() where window[i - 1].chargerPowerKw > 0 {
            let dt = window[i].at.timeIntervalSince(window[i - 1].at)
            if dt > 0 { active += min(dt, activeStepCap) }
        }

        // From the start to the first sample within 90% of the session's peak (a DC session ramps in seconds).
        var timeToPeak = 0.0
        if charge.maxPowerKw > 0, let first = window.first(where: { $0.chargerPowerKw >= charge.maxPowerKw * 0.9 }) {
            timeToPeak = max(0, first.at.timeIntervalSince(charge.startedAt))
        }

        // The limit in force: the most common one over the session (the user may change it part way, so the mode beats
        // the first or the last); the first seen wins a tie.
        var counts: [Int: Int] = [:]
        var order: [Int] = []
        for s in window {
            guard let v = s.chargeLimitPct, v.isFinite else { continue }
            let l = Int(v.rounded())
            if l <= 0 { continue }
            if counts[l] == nil { order.append(l) }
            counts[l, default: 0] += 1
        }
        var limit: Int?
        var best = 0
        for l in order where counts[l, default: 0] > best {
            limit = l
            best = counts[l, default: 0]
        }

        let gained = max(0, charge.endSocPct - charge.startSocPct)
        let hours = totalSec / 3600

        return ChargeInsights(
            tier: tier(maxKw: charge.maxPowerKw),
            timeToPeakSec: timeToPeak,
            activeSec: active,
            activePct: totalSec > 0 ? active / totalSec * 100 : 0,
            socGainedPct: gained,
            socPerHour: hours > 0 ? gained / hours : 0,
            limitPct: limit,
            costPerKwh: costPerKwh(charge),
            outsideTempC: mean(window.map(\.outsideTempC)),
            insideTempC: mean(window.map(\.insideTempC))
        )
    }

    /// The price per kWh from the charge's cost (billed or at your rate) or, failing that, its estimate; nil with no
    /// price or no energy. A "~" says it is an estimate.
    static func costPerKwh(_ charge: ActivityCharge) -> String? {
        guard charge.energyAddedKwh > 0 else { return nil }
        let amount: Double
        let currency: String
        let estimate: Bool
        switch ActivityLogic.costLine(charge).tone {
        case .billed:
            amount = charge.cost; currency = charge.currency; estimate = false
        case .estimate:
            if charge.cost > 0 { amount = charge.cost; currency = charge.currency } else {
                amount = charge.estimatedCost ?? 0; currency = charge.estimatedCurrency ?? ""
            }
            estimate = true
        case .free, .missing:
            return nil
        }
        guard amount > 0 else { return nil }
        let code = currency.count == 3 ? currency : "USD"
        return "\(estimate ? "~" : "")\(NumberText.money(amount / charge.energyAddedKwh, code, digits: 3))/kWh"
    }

    /// The plain mean of the readings that are there: 0 is the recorder's "not reported", so it doesn't count.
    static func mean(_ values: [Double?]) -> Double? {
        let usable = values.compactMap { $0 }.filter { $0.isFinite && $0 != 0 }
        return usable.isEmpty ? nil : usable.reduce(0, +) / Double(usable.count)
    }
}
