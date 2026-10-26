import Foundation

struct DayValue: Equatable, Identifiable {
    /// A local calendar day (its start).
    let day: Date
    let value: Double
    var id: Date { day }
}

struct SocPoint: Equatable, Identifiable {
    let at: Date
    let pct: Double
    var id: Date { at }
}

/// The chart maths, the same as the web's `analytics.ts`, without any view code so it can be tested.
enum ChartMath {
    /// A round top and round ticks from 0 for a chart whose data tops out at `max`, the same as the web's `niceAxis`: the step is 1, 2,
    /// 2.5 or 5 times a power of ten (whole numbers only), and the one with the lowest top that gives two or three intervals wins; four
    /// only when nothing else fits. 509 gives 0 · 200 · 400 · 600.
    static func niceAxis(max value: Double) -> (top: Double, ticks: [Double]) {
        let m = value.isFinite && value > 0 ? value : 1
        var steps: [Double] = []
        var p = 1.0
        while p <= 1e9 {
            for k in [1.0, 2.0, 2.5, 5.0] where (k * p).rounded() == k * p { steps.append(k * p) }
            p *= 10
        }
        func best(upTo maxIntervals: Int) -> (step: Double, n: Int)? {
            steps.map { (step: $0, n: Swift.max(1, Int((m / $0 - 1e-9).rounded(.up)))) }
                .filter { $0.n >= 2 && $0.n <= maxIntervals }
                .min { a, b in a.step * Double(a.n) != b.step * Double(b.n) ? a.step * Double(a.n) < b.step * Double(b.n) : a.n < b.n }
        }
        let pick = best(upTo: 3) ?? best(upTo: 4) ?? (step: m.rounded(.up), n: 1)
        return (pick.step * Double(pick.n), (0...pick.n).map { pick.step * Double($0) })
    }

    /// Miles driven on each of `days` (local days), bucketed by the local day each drive started.
    static func milesPerDay(_ drives: [DrivePoint], over days: [Date], calendar: Calendar) -> [DayValue] {
        var buckets: [Date: Double] = [:]
        for d in drives {
            buckets[calendar.startOfDay(for: d.startedAt), default: 0] += max(0, d.distanceMi)
        }
        return days.map { DayValue(day: $0, value: buckets[$0] ?? 0) }
    }

    /// Whether the charge limit is drawn from today's value: only a real limit (above 0, below 100) and only on the month
    /// that contains now. The limit is what the truck is set to today; an earlier month may have had another.
    static func limitVisible(_ limit: Double?, now: Date, since: Date, until: Date) -> Bool {
        guard let limit, limit > 0, limit < 100 else { return false }
        return now >= since && now < until
    }

    /// A stretch of the charge-limit line: the limit held from `start` to `end`.
    struct LimitStep: Equatable {
        let start: Date
        let end: Date
        let pct: Double
    }

    static func validLimit(_ pct: Double?) -> Bool {
        guard let pct else { return false }
        return pct.isFinite && pct > 0 && pct <= 100
    }

    /// The charge limit over `[from, to)` as steps, from the limits stored on the charges (the web's `limitSteps`, the
    /// same rule): each charge with a limit starts a step at its start, held to the next one with a limit (a charge
    /// without one carries the step on); nothing before the period's first such charge. After the last charge's end, up
    /// to now, the live target when known (the current period only), else that charge's limit. Steps end at now, so
    /// days to come stay empty. Neighbouring steps at the same value are joined.
    static func limitSteps(_ charges: [ChargePoint], from: Date, to: Date, now: Date, live: Double?) -> [LimitStep] {
        let end = min(to, now)
        let set = charges
            .filter { validLimit($0.chargeLimitPct) && $0.startedAt >= from && $0.startedAt < end }
            .sorted { $0.startedAt < $1.startedAt }
        var out: [LimitStep] = []
        func push(_ start: Date, _ stop: Date, _ pct: Double) {
            guard stop > start else { return }
            if let last = out.last, last.pct == pct, last.end >= start {
                out[out.count - 1] = LimitStep(start: last.start, end: max(last.end, stop), pct: pct)
            } else {
                out.append(LimitStep(start: start, end: stop, pct: pct))
            }
        }
        for (i, c) in set.enumerated() {
            guard let pct = c.chargeLimitPct else { continue }
            if i + 1 < set.count {
                push(c.startedAt, set[i + 1].startedAt, pct)
                continue
            }
            let held = min(max(c.endedAt, c.startedAt), end)
            push(c.startedAt, held, pct)
            let current = now >= from && now <= to
            push(held, end, current && validLimit(live) ? (live ?? pct) : pct)
        }
        return out
    }

    /// The one stretch of limit the calm Battery chart draws: from the last limit change (the last step) to now, or to the
    /// end of an earlier month, carrying that limit. Without any limit stored on the charges (an older server), today's
    /// target over the last two days on the current month only; nil for none. A line across the whole month would claim
    /// the limit held all month; it is not drawn.
    static func limitSegment(_ charges: [ChargePoint], from: Date, to: Date, now: Date, live: Double?) -> LimitStep? {
        if let last = limitSteps(charges, from: from, to: to, now: now, live: live).last { return last }
        guard limitVisible(live, now: now, since: from, until: to), let live else { return nil }
        return LimitStep(start: max(from, now.addingTimeInterval(-2 * 86_400)), end: now, pct: live)
    }

    /// Both ends of every drive and charge, oldest first. A reading of 0 or less is "unknown", as on the web.
    static func socTrend(drives: [DrivePoint], charges: [ChargePoint]) -> [SocPoint] {
        var pts: [SocPoint] = []
        func add(_ at: Date, _ pct: Double) {
            if pct.isFinite, pct > 0 { pts.append(SocPoint(at: at, pct: pct)) }
        }
        for d in drives { add(d.startedAt, d.startSocPct); add(d.endedAt, d.endSocPct) }
        for c in charges { add(c.startedAt, c.startSocPct); add(c.endedAt, c.endSocPct) }
        return pts.sorted { $0.at < $1.at }
    }

    /// Idle-awake minutes per day. The server's days are UTC dates; each is placed on the same calendar date locally.
    static func idleMinutes(_ days: [DayActivity], calendar: Calendar) -> [DayValue] {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC") ?? .gmt
        return days.compactMap { d in
            let c = utc.dateComponents([.year, .month, .day], from: d.day)
            guard let local = calendar.date(from: c) else { return nil }
            return DayValue(day: calendar.startOfDay(for: local), value: max(0, d.idleAwakeH * 60))
        }
    }

    /// The average over completed days (today is partial, so it would pull the figure down); nil with none.
    static func averageIdle(_ bars: [DayValue], now: Date, calendar: Calendar) -> Double? {
        let today = calendar.startOfDay(for: now)
        let full = bars.filter { $0.day < today }
        guard !full.isEmpty else { return nil }
        return full.reduce(0) { $0 + $1.value } / Double(full.count)
    }

    /// Minutes said in words: "1 hour 12 minutes".
    static func spokenMinutes(_ minutes: Double) -> String {
        let m = max(0, Int(minutes.rounded()))
        let h = m / 60, r = m % 60
        func unit(_ n: Int, _ one: String) -> String { "\(n) \(one)\(n == 1 ? "" : "s")" }
        if h == 0 { return unit(r, "minute") }
        return r == 0 ? unit(h, "hour") : "\(unit(h, "hour")) \(unit(r, "minute"))"
    }

    /// The weekday name of a local day, in the phone's language.
    static func weekday(_ day: Date, calendar: Calendar) -> String {
        let f = DateFormatter()
        f.calendar = calendar
        f.timeZone = calendar.timeZone
        f.dateFormat = "EEEE"
        return f.string(from: day)
    }

    /// What VoiceOver says for a per-day chart: the period, the average over the days shown and the biggest day.
    /// `format` turns a value into words ("28 miles").
    static func spokenDaily(title: String, period: String, bars: [DayValue], calendar: Calendar, format: (Double) -> String) -> (label: String, value: String) {
        let label = "\(title), \(period)"
        guard !bars.isEmpty else { return (label, "") }
        let avg = bars.reduce(0) { $0 + $1.value } / Double(bars.count)
        var value = "average \(format(avg))"
        if let top = bars.max(by: { $0.value < $1.value }), top.value > 0 {
            value += ", most \(format(top.value)) on \(weekday(top.day, calendar: calendar))"
        }
        return (label, value)
    }

}
