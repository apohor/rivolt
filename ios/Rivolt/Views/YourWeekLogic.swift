import Foundation

/// "Your week" on Now, the iPhone twin of the web's strip: this week's miles, energy and charging cost, and how the miles compare with
/// last week up to the same moment of the week. Monday to Sunday in the phone's calendar, from the same Activity answer the tiles read.
/// Nothing compared with other owners; no new data.
enum YourWeekLogic {
    /// Last week needs at least this many miles before a percentage is worth saying (no "up 400%" from an empty week).
    static let minLastWeekMiles = 5.0

    struct Figures: Equatable {
        var miles = 0.0
        var kWh = 0.0
        var cost = 0.0
        var currency = "USD"
        var drives = 0
        var charges = 0
        var costEstimated = false
    }

    struct Week: Equatable {
        let figures: Figures
        /// Miles against last week at the same moment, a whole percent; nil unless last week gives something to compare with.
        let milesChangePct: Int?
    }

    /// Cost sources the owner or Rivian stated; any other priced charge is an estimate.
    private static let stated: Set<String> = ["rivian", "user"]

    /// A Monday-first calendar in the caller's zone.
    static func mondayCalendar(_ base: Calendar) -> Calendar {
        var c = base
        c.firstWeekday = 2
        c.minimumDaysInFirstWeek = 4
        return c
    }

    /// Monday 00:00 of the week holding `date`.
    static func weekStart(_ date: Date, calendar: Calendar) -> Date {
        let c = mondayCalendar(calendar)
        return c.dateInterval(of: .weekOfYear, for: date)?.start ?? c.startOfDay(for: date)
    }

    static func figures(drives: [ActivityDrive], charges: [ActivityCharge], from: Date, to: Date) -> Figures {
        let ds = drives.filter { $0.startedAt >= from && $0.startedAt < to }
        let cs = charges.filter { $0.startedAt >= from && $0.startedAt < to }
        var f = Figures()
        f.drives = ds.count
        f.charges = cs.count
        f.miles = ds.reduce(0) { $0 + $1.distanceMi }
        f.kWh = cs.reduce(0) { $0 + $1.energyAddedKwh }
        var perCurrency: [String: Int] = [:]
        for c in cs {
            if c.cost > 0 {
                f.cost += c.cost; perCurrency[c.currency, default: 0] += 1
                if !stated.contains(c.costSourceEffective ?? "") { f.costEstimated = true }
            } else if let e = c.estimatedCost, e > 0 {
                f.cost += e; perCurrency[c.estimatedCurrency ?? "", default: 0] += 1
                f.costEstimated = true
            }
        }
        if let best = perCurrency.filter({ !$0.key.isEmpty }).max(by: { $0.value == $1.value ? $0.key > $1.key : $0.value < $1.value }) { f.currency = best.key }
        return f
    }

    /// Nil when nothing was recorded this week (no strip). `drives` and `charges` must reach back to last Monday.
    static func week(drives: [ActivityDrive], charges: [ActivityCharge], now: Date, calendar: Calendar) -> Week? {
        let start = weekStart(now, calendar: calendar)
        let f = figures(drives: drives, charges: charges, from: start, to: now.addingTimeInterval(1))
        guard f.drives > 0 || f.charges > 0 else { return nil }
        let lastStart = calendar.date(byAdding: .day, value: -7, to: start) ?? start.addingTimeInterval(-7 * 86_400)
        let lastEnd = lastStart.addingTimeInterval(now.timeIntervalSince(start) + 1)
        let last = figures(drives: drives, charges: charges, from: lastStart, to: lastEnd)
        let pct: Int? = last.miles >= minLastWeekMiles && f.miles > 0 ? Int(((f.miles - last.miles) / last.miles * 100).rounded()) : nil
        return Week(figures: f, milesChangePct: pct)
    }

    static func changeLine(_ pct: Int?) -> String? {
        guard let pct else { return nil }
        if pct == 0 { return "About the same miles as last week so far" }
        return "\(pct > 0 ? "▲" : "▼") \(abs(pct))% miles vs last week so far"
    }

    static let title = "Your week"
}
