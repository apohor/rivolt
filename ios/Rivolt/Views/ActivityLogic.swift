import Foundation

enum ActivityFilter: String, CaseIterable, Identifiable {
    case all, drives, charges
    var id: String { rawValue }
    var label: String {
        switch self {
        case .all: return "All"
        case .drives: return "Drives"
        case .charges: return "Charges"
        }
    }
}

enum ActivityItem: Equatable, Identifiable {
    case drive(ActivityDrive)
    case charge(ActivityCharge)

    var id: String {
        switch self {
        case .drive(let d): return "drive:\(d.id)"
        case .charge(let c): return "charge:\(c.id)"
        }
    }

    var startedAt: Date {
        switch self {
        case .drive(let d): return d.startedAt
        case .charge(let c): return c.startedAt
        }
    }
}

struct ActivityDay: Equatable, Identifiable {
    let day: Date
    let items: [ActivityItem]
    var id: Date { day }
}

/// How a charge's cost reads, from the ledger's source: the same rules as the web's `chargeCostLine`.
struct ChargeCostLine: Equatable {
    enum Tone: Equatable { case billed, estimate, free, missing }
    let text: String
    let tone: Tone
}

enum ActivityLogic {
    /// Newest first, grouped by the local day each started.
    static func days(_ r: ActivityResponse, filter: ActivityFilter, calendar: Calendar) -> [ActivityDay] {
        var items: [ActivityItem] = []
        if filter != .charges { items += r.drives.map(ActivityItem.drive) }
        if filter != .drives { items += r.charges.map(ActivityItem.charge) }
        items.sort { $0.startedAt > $1.startedAt }
        var order: [Date] = []
        var buckets: [Date: [ActivityItem]] = [:]
        for it in items {
            let day = calendar.startOfDay(for: it.startedAt)
            if buckets[day] == nil { order.append(day) }
            buckets[day, default: []].append(it)
        }
        return order.map { ActivityDay(day: $0, items: buckets[$0] ?? []) }
    }

    /// Home and Away are AC charges by place; Fast is a DC fast charge wherever it was.
    static func placeLabel(_ c: ActivityCharge) -> String? {
        if c.kind == "dcfc" { return "Fast" }
        if c.kind == "ac" {
            if c.place == "home" { return "Home" }
            if c.place == "away" { return "Away" }
        }
        return nil
    }

    static func costLine(_ c: ActivityCharge) -> ChargeCostLine {
        let money = NowFormat.money(c.cost, currency: usable(c.currency))
        let est = ChargeCostLine(text: "~\(money) (estimate)", tone: .estimate)
        let unpriced = ChargeCostLine(text: "Unpriced", tone: .missing)
        func estimatedField() -> ChargeCostLine {
            if let e = c.estimatedCost, e > 0 {
                return ChargeCostLine(text: "~\(NowFormat.money(e, currency: usable(c.estimatedCurrency))) (estimate)", tone: .estimate)
            }
            return unpriced
        }
        switch c.costSourceEffective ?? "" {
        case "unknown": return unpriced
        case "home_rate_misapplied": return ChargeCostLine(text: "Estimate pending", tone: .missing)
        case "user": return c.cost == 0 ? ChargeCostLine(text: "Free", tone: .free) : ChargeCostLine(text: money, tone: .billed)
        case "network_estimate": return c.cost > 0 ? est : unpriced
        case "rivian", "home_rate", "observed_estimate":
            // A derived source was worked out from the home rate rather than recorded: an estimate.
            if c.cost > 0 { return c.costSourceDerived == true ? est : ChargeCostLine(text: money, tone: .billed) }
            return estimatedField()
        default:
            if c.cost > 0 { return ChargeCostLine(text: money, tone: .billed) }
            return estimatedField()
        }
    }

    /// The web falls back to USD when a row carries no currency.
    private static func usable(_ currency: String?) -> String {
        guard let currency, currency.count == 3 else { return "USD" }
        return currency
    }

    /// A drive that used battery but has no energy reading: shown and flagged, never given a made-up efficiency or cost.
    static func missingEnergy(_ d: ActivityDrive) -> Bool {
        d.distanceMi > 0.1 && !(d.energyUsedKwh > 0)
    }

    static func efficiency(_ d: ActivityDrive) -> Double? {
        d.energyUsedKwh > 0 && d.distanceMi > 0 ? d.distanceMi / d.energyUsedKwh : nil
    }

    /// Wall-clock length of a drive; a merged round trip counts its driving time, not the time parked between legs.
    static func duration(_ d: ActivityDrive) -> TimeInterval? {
        if let s = d.drivingSeconds, s > 0 { return s }
        let t = d.endedAt.timeIntervalSince(d.startedAt)
        return t > 0 && d.endedAt.timeIntervalSince1970 > 946_684_800 ? t : nil
    }

    static func duration(_ c: ActivityCharge) -> TimeInterval? {
        let t = c.endedAt.timeIntervalSince(c.startedAt)
        return t > 0 && c.endedAt.timeIntervalSince1970 > 946_684_800 ? t : nil
    }
}

// MARK: What VoiceOver says for a row

extension ActivityLogic {
    static func spoken(_ d: ActivityDrive, calendar: Calendar) -> String {
        var parts = ["Drive, \(timeOfDay(d.startedAt, calendar: calendar))."]
        parts.append("\(NowFormat.distance(mi: d.distanceMi, spoken: true)).")
        if let legs = d.roundTripLegs, legs > 1 { parts.append("Round trip, \(legs) legs.") }
        if let t = duration(d) { parts.append("\(NowFormat.spokenDuration(t)).") }
        if missingEnergy(d) {
            parts.append("No energy reading.")
        } else if let e = efficiency(d) {
            parts.append("\(NowFormat.efficiency(miPerKwh: e, spoken: true)).")
        }
        if !missingEnergy(d), let c = d.estimatedCost, c > 0, let m = NowFormat.moneyIfKnown(c, currency: d.estimatedCurrency) {
            parts.append("Cost about \(m), an estimate.")
        }
        return parts.joined(separator: " ")
    }

    static func spoken(_ c: ActivityCharge, calendar: Calendar) -> String {
        var parts = ["Charge, \(timeOfDay(c.startedAt, calendar: calendar))."]
        if let p = placeLabel(c) { parts.append(p == "Fast" ? "Fast charger." : p == "Home" ? "At home." : "Away from home.") }
        parts.append("\(NowFormat.oneDecimal(c.energyAddedKwh)) kilowatt hours added.")
        let line = costLine(c)
        switch line.tone {
        case .estimate: parts.append("Cost about \(line.text.replacingOccurrences(of: "~", with: "").replacingOccurrences(of: " (estimate)", with: "")), an estimate.")
        case .billed: parts.append("Cost \(line.text).")
        case .free: parts.append("Free.")
        case .missing: parts.append("\(line.text).")
        }
        return parts.joined(separator: " ")
    }

    static func timeOfDay(_ date: Date, calendar: Calendar) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).hour().minute()
        f.calendar = calendar
        return date.formatted(f)
    }
}

// MARK: The feed's wording and the detail tiles

extension ActivityLogic {
    /// "26m" or "3h 32m", the web's short duration.
    static func shortDuration(_ seconds: TimeInterval) -> String {
        let m = max(0, Int(seconds / 60))
        return m >= 60 ? "\(m / 60)h \(m % 60)m" : "\(m)m"
    }

    /// "7:48 PM–8:14 PM", or just the start for something with no usable end.
    static func timeRange(start: Date, end: Date, calendar: Calendar) -> String {
        let s = timeOfDay(start, calendar: calendar)
        guard end.timeIntervalSince1970 > 946_684_800, end > start else { return s }
        return "\(s)–\(timeOfDay(end, calendar: calendar))"
    }

    /// "Away charge", "Home charge", "Fast charge" or "Charge", the web's titles.
    static func chargeTitle(_ c: ActivityCharge) -> String {
        switch placeLabel(c) {
        case "Home": return "Home charge"
        case "Away": return "Away charge"
        case "Fast": return "Fast charge"
        default: return "Charge"
        }
    }

    /// "79→80%" when both levels are known.
    static func levelChange(start: Double, end: Double) -> String? {
        guard start > 0, end > 0 else { return nil }
        return "\(Int(start.rounded()))→\(Int(end.rounded()))%"
    }

    // MARK: Detail tiles

    static func tiles(_ d: ActivityDrive, metric: Bool) -> [KpiTile] {
        let k = metric ? 1.609344 : 1
        let dist = metric ? "km" : "mi"
        func f(_ v: Double, _ digits: Int) -> String { v.formatted(.number.precision(.fractionLength(digits))) }
        var out: [KpiTile] = []
        out.append(KpiTile(id: "distance", label: "Distance", value: f(d.distanceMi * k, 1), unit: dist, hint: nil))
        if let t = duration(d) { out.append(KpiTile(id: "duration", label: "Duration", value: shortDuration(t), unit: "", hint: nil)) }
        if d.startSocPct > 0, d.endSocPct > 0 {
            out.append(KpiTile(id: "soc", label: "Charge level", value: "\(Int(d.startSocPct.rounded()))% → \(Int(d.endSocPct.rounded()))%", unit: "", hint: nil))
        }
        if d.avgSpeedMph > 0 || d.maxSpeedMph > 0 {
            out.append(KpiTile(id: "speed", label: "Speed avg / max", value: "\(f(d.avgSpeedMph * k, 0)) / \(f(d.maxSpeedMph * k, 0))", unit: metric ? "km/h" : "mph", hint: nil))
        }
        if missingEnergy(d) {
            out.append(KpiTile(id: "energy", label: "Energy", value: "—", unit: "", hint: "no energy reading"))
        } else {
            if d.energyUsedKwh > 0 { out.append(KpiTile(id: "energy", label: "Energy", value: f(d.energyUsedKwh, 1), unit: "kWh", hint: nil)) }
            if let c = d.estimatedCost, c > 0, let m = NowFormat.moneyIfKnown(c, currency: d.estimatedCurrency) {
                var hint = "estimate"
                if let p = d.estimatedPricePerKwh, p > 0 { hint = "at ~\(f(p, 3)) \(d.estimatedCurrency ?? "")/kWh from your most recent charge" }
                out.append(KpiTile(id: "cost", label: "Cost", value: "~\(m)", unit: "", hint: hint))
            }
            if let e = efficiency(d) {
                out.append(KpiTile(id: "efficiency", label: "Efficiency", value: f(e * k, 2), unit: metric ? "km/kWh" : "mi/kWh", hint: nil))
            }
        }
        return out
    }

    static func tiles(_ c: ActivityCharge) -> [KpiTile] {
        func f(_ v: Double, _ digits: Int) -> String { v.formatted(.number.precision(.fractionLength(digits))) }
        var out: [KpiTile] = []
        if let t = duration(c) { out.append(KpiTile(id: "duration", label: "Duration", value: shortDuration(t), unit: "", hint: nil)) }
        if c.startSocPct > 0, c.endSocPct > 0 {
            out.append(KpiTile(id: "soc", label: "Charge level", value: "\(Int(c.startSocPct.rounded()))% → \(Int(c.endSocPct.rounded()))%", unit: "", hint: nil))
        }
        out.append(KpiTile(id: "energy", label: "Energy", value: f(c.energyAddedKwh, 1), unit: "kWh", hint: nil))
        if c.maxPowerKw > 0 { out.append(KpiTile(id: "power", label: "Max kW", value: f(c.maxPowerKw, 1), unit: "kW", hint: nil)) }
        let line = costLine(c)
        switch line.tone {
        case .missing: out.append(KpiTile(id: "cost", label: "Cost", value: "—", unit: "", hint: line.text == "Unpriced" ? "price unknown" : line.text))
        case .estimate: out.append(KpiTile(id: "cost", label: "Cost", value: line.text.replacingOccurrences(of: " (estimate)", with: ""), unit: "", hint: "estimate"))
        case .free: out.append(KpiTile(id: "cost", label: "Cost", value: "Free", unit: "", hint: nil))
        case .billed: out.append(KpiTile(id: "cost", label: "Cost", value: line.text, unit: "", hint: c.costSourceEffective == "rivian" ? "billed" : "at your own rate"))
        }
        return out
    }
}
