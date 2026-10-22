import Foundation

/// The figures under the Now hero, worked out the web's way (`driveStats`, `chargeStats`, the Home page's tiles) from
/// the month's drives and charges, the ledger's spend and the sleep record. No view code, so it can be tested.
struct KpiDriveStats: Equatable {
    let count: Int
    let miles: Double
    let avgTripMi: Double
    let energyUsedKwh: Double
    let milesForEnergy: Double
    let miPerKwh: Double
    let milesPerPct: Double
    let maxSpeedMph: Double
}

struct KpiChargeStats: Equatable {
    let count: Int
    let energyKwh: Double
    let maxPowerKw: Double
    let cost: Double
    let currency: String
}

/// Miles driven since the last finished charge, how many drives that was and when the charge ended.
struct SinceCharge: Equatable {
    let miles: Double
    let drives: Int
    let since: Date
}

struct KpiTile: Equatable, Identifiable {
    let id: String
    let label: String
    /// The number, and its unit apart so the unit can be set small beside it; the unit is empty when there is none.
    let value: String
    let unit: String
    let hint: String?

    /// One sentence for VoiceOver, with the units and symbols as words.
    var spoken: String {
        var parts = ["\(label), \(value) \(unit)".trimmingCharacters(in: .whitespaces)]
        if let hint { parts.append(hint) }
        return SpokenText.words(parts.joined(separator: ". "))
    }
}

enum KpiLogic {
    static func drives(_ ds: [ActivityDrive]) -> KpiDriveStats {
        let miles = ds.reduce(0) { $0 + max(0, $1.distanceMi) }
        let socUsed = ds.reduce(0) { $0 + max(0, $1.startSocPct - $1.endSocPct) }
        let withEnergy = ds.filter { $0.energyUsedKwh > 0 && $0.distanceMi > 0 }
        let energy = withEnergy.reduce(0) { $0 + $1.energyUsedKwh }
        let milesForEnergy = withEnergy.reduce(0) { $0 + $1.distanceMi }
        return KpiDriveStats(
            count: ds.count, miles: miles, avgTripMi: ds.isEmpty ? 0 : miles / Double(ds.count),
            energyUsedKwh: energy, milesForEnergy: milesForEnergy, miPerKwh: energy > 0 ? milesForEnergy / energy : 0,
            milesPerPct: socUsed > 0 ? miles / socUsed : 0, maxSpeedMph: ds.reduce(0) { max($0, $1.maxSpeedMph) })
    }

    static func charges(_ cs: [ActivityCharge]) -> KpiChargeStats {
        var cost = 0.0
        var currencies: [String: Int] = [:]
        for c in cs {
            if c.cost > 0 {
                cost += c.cost
                currencies[c.currency, default: 0] += 1
            } else if let e = c.estimatedCost, e > 0 {
                cost += e
                currencies[c.estimatedCurrency ?? "", default: 0] += 1
            }
        }
        return KpiChargeStats(
            count: cs.count, energyKwh: cs.reduce(0) { $0 + max(0, $1.energyAddedKwh) },
            maxPowerKw: cs.reduce(0) { max($0, $1.maxPowerKw) }, cost: cost, currency: dominant(currencies))
    }

    /// The currency most rows used; ties go to the first alphabetically so the result is the same on every render.
    static func dominant(_ counts: [String: Int]) -> String {
        var best = "", bestN = 0
        for (cur, n) in counts where n > bestN || (n == bestN && cur < best) {
            best = cur
            bestN = n
        }
        return best
    }

    /// The end of the newest finished charge: the in-progress one (the row that began with the current session) doesn't
    /// count, and a row with no usable end time can't say when charging stopped.
    static func lastFinishedChargeEnd(_ charges: [ChargeBrief], activeStart: Date?) -> Date? {
        for c in charges {
            if let active = activeStart, let start = c.startedAt, abs(start.timeIntervalSince(active)) <= ChargingPlace.tolerance { continue }
            if let end = c.endedAt, end.timeIntervalSince1970 > 946_684_800 { return end }
        }
        return nil
    }

    static func sinceCharge(end: Date, points: ChartPoints) -> SinceCharge {
        let ds = points.drives.filter { $0.startedAt > end }
        return SinceCharge(miles: ds.reduce(0) { $0 + max(0, $1.distanceMi) }, drives: ds.count, since: end)
    }

    // MARK: The five text tiles

    /// A figure with its unit, or a dash for nothing (the web's `num`: zero and non-numbers read as a dash).
    private static func figure(_ v: Double, digits: Int, unit: String) -> (value: String, unit: String) {
        guard v.isFinite, v != 0 else { return ("—", "") }
        return (v.formatted(.number.precision(.fractionLength(digits))), unit)
    }

    private static func fixed(_ v: Double, _ digits: Int) -> String { v.formatted(.number.precision(.fractionLength(digits))) }

    static func tiles(drives d: KpiDriveStats, charges c: KpiChargeStats, since: SinceCharge?, sinceKnown: Bool,
                      ledger: LedgerMonthSpend?, ledgerPending: Bool, metric: Bool, calendar: Calendar = .current) -> [KpiTile] {
        let k = metric ? 1.609344 : 1
        let dist = metric ? "km" : "mi"
        func dst(_ mi: Double, _ digits: Int) -> String { fixed(mi * k, digits) }

        var out: [KpiTile] = []

        // Since last charge
        let sc = since.map { figure($0.miles * k, digits: 1, unit: dist) } ?? ("—", "")
        var scHint = "no finished charges yet"
        if let since {
            var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.abbreviated).day()
            f.calendar = calendar
            scHint = "\(since.drives) \(since.drives == 1 ? "drive" : "drives") · since \(since.since.formatted(f))"
        } else if !sinceKnown {
            scHint = ""
        }
        out.append(KpiTile(id: "since", label: "Since last charge", value: sc.0, unit: sc.1, hint: scHint.isEmpty ? nil : scHint))

        // Miles
        let m = figure(d.miles * k, digits: 1, unit: dist)
        out.append(KpiTile(id: "miles", label: metric ? "Kilometres" : "Miles", value: m.0, unit: m.1,
                           hint: "\(d.count) \(d.count == 1 ? "drive" : "drives") · avg \(dst(d.avgTripMi, 1)) \(dist)"))

        // Energy added
        let e = figure(c.energyKwh, digits: 1, unit: "kWh")
        out.append(KpiTile(id: "energy", label: "Energy added", value: e.0, unit: e.1,
                           hint: "\(c.count) \(c.count == 1 ? "charge" : "charges") · peak \(fixed(c.maxPowerKw, 0)) kW"))

        // Efficiency: the pack's own figure, else wall to wheel, else miles per percent.
        let effUnit = metric ? "km/kWh" : "mi/kWh"
        if d.miPerKwh > 0 {
            out.append(KpiTile(id: "efficiency", label: "Efficiency", value: fixed(d.miPerKwh * k, 2), unit: effUnit,
                               hint: "pack-side · \(dst(d.milesForEnergy, 0)) \(dist) / \(fixed(d.energyUsedKwh, 0)) kWh"))
        } else if c.energyKwh > 0, d.miles > 0 {
            out.append(KpiTile(id: "efficiency", label: "Efficiency", value: fixed(d.miles / c.energyKwh * k, 2), unit: effUnit,
                               hint: "wall-to-wheel · \(dst(d.miles, 0)) \(dist) / \(fixed(c.energyKwh, 0)) kWh"))
        } else if d.milesPerPct > 0 {
            out.append(KpiTile(id: "efficiency", label: "Efficiency", value: fixed(d.milesPerPct * k, 2), unit: "\(dist)/%",
                               hint: "top speed \(fixed(d.maxSpeedMph * k, 0)) \(metric ? "km/h" : "mph")"))
        } else {
            out.append(KpiTile(id: "efficiency", label: "Efficiency", value: "—", unit: "",
                               hint: "top speed \(fixed(d.maxSpeedMph * k, 0)) \(metric ? "km/h" : "mph")"))
        }

        // Charging cost: the ledger's month, the number Money shows; the month's charges when the ledger can't say.
        let spend: (total: Double, currency: String)?
        if let ledger { spend = (ledger.statementTotal, ledger.currency) }
        else if ledgerPending { spend = nil }
        else { spend = (c.cost, c.currency.isEmpty ? "USD" : c.currency) }
        if let spend {
            let sessions = "\(c.count) \(c.count == 1 ? "session" : "sessions")"
            var hint = "no priced charges yet"
            if spend.total > 0 {
                hint = d.miles > 0 ? "\(fixed(spend.total / (d.miles * k), 3)) \(spend.currency)/\(dist) · \(sessions)" : sessions
            }
            out.append(KpiTile(id: "cost", label: "Charging cost", value: NowFormat.money(spend.total, currency: spend.currency), unit: "", hint: hint))
        } else {
            out.append(KpiTile(id: "cost", label: "Charging cost", value: "—", unit: "", hint: nil))
        }
        return out
    }
}

// MARK: Idle awake

/// The sixth tile: today's idle-awake minutes, the average over completed days and a mini chart of the last seven days.
struct IdleAwakeTile: Equatable {
    let today: Double?
    let average: Double?
    let averageDays: Int
    let bars: [DayValue]

    static func make(sleep: [DayActivity]?, now: Date, calendar: Calendar) -> IdleAwakeTile? {
        guard let sleep else { return nil }
        let all = ChartMath.idleMinutes(sleep, calendar: calendar)
        let todayStart = calendar.startOfDay(for: now)
        let today = all.last.flatMap { $0.day == todayStart ? $0.value : nil }
        let full = all.filter { $0.day < todayStart }
        let avg = full.isEmpty ? nil : full.reduce(0) { $0 + $1.value } / Double(full.count)
        return IdleAwakeTile(today: today, average: avg, averageDays: full.count, bars: Array(all.suffix(7)))
    }
}
