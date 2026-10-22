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
    /// Miles driven on each of `days` (local days), bucketed by the local day each drive started.
    static func milesPerDay(_ drives: [DrivePoint], over days: [Date], calendar: Calendar) -> [DayValue] {
        var buckets: [Date: Double] = [:]
        for d in drives {
            buckets[calendar.startOfDay(for: d.startedAt), default: 0] += max(0, d.distanceMi)
        }
        return days.map { DayValue(day: $0, value: buckets[$0] ?? 0) }
    }

    /// The stretches the truck was charging or driving, for the bands behind the charge-level line.
    struct Band: Equatable, Identifiable {
        enum Kind { case charging, driving }
        let kind: Kind
        let start: Date
        let end: Date
        var id: String { "\(kind)-\(start.timeIntervalSince1970)" }
    }

    /// Charging and driving stretches within `[from, to]`, clipped to it; empty or backwards ones are left out.
    /// `minSpan` widens a shorter stretch (to the right, still inside `to`) so a short drive is a visible mark at month
    /// scale, as the web's minimum band width does.
    static func bands(drives: [DrivePoint], charges: [ChargePoint], from: Date, to: Date, minSpan: TimeInterval = 0) -> [Band] {
        func make(_ kind: Band.Kind, _ s: Date, _ e: Date) -> Band? {
            let start = max(s, from)
            guard min(e, to) > start else { return nil }
            let end = min(max(e, start.addingTimeInterval(minSpan)), to)
            return Band(kind: kind, start: start, end: end)
        }
        let all = charges.compactMap { make(.charging, $0.startedAt, $0.endedAt) } + drives.compactMap { make(.driving, $0.startedAt, $0.endedAt) }
        return all.sorted { $0.start < $1.start }
    }

    /// Whether the charge limit is drawn: only a real limit (above 0, below 100) and only on the month that contains
    /// now. The limit is what the truck is set to today; an earlier month, or the earlier days of this one, may have
    /// had another (it changes), so a line across them would say something we don't know.
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

    /// The highest and lowest points of the line, for the two labelled dots; nil with fewer than two points.
    static func highLow(_ trend: [SocPoint]) -> (high: SocPoint, low: SocPoint)? {
        guard trend.count >= 2, let high = trend.max(by: { $0.pct < $1.pct }), let low = trend.min(by: { $0.pct < $1.pct }),
              high != low else { return nil }
        return (high, low)
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

    /// For the charge-level line: where it starts and ends, and its lowest and highest points.
    static func spokenLevel(period: String, trend: [SocPoint]) -> (label: String, value: String) {
        let label = "Charge level, \(period)"
        guard let first = trend.first, let last = trend.last,
              let low = trend.min(by: { $0.pct < $1.pct }), let high = trend.max(by: { $0.pct < $1.pct }) else { return (label, "") }
        func pc(_ v: Double) -> String { "\(Int(v.rounded())) percent" }
        return (label, "from \(pc(first.pct)) to \(pc(last.pct)), lowest \(pc(low.pct)), highest \(pc(high.pct))")
    }
}
