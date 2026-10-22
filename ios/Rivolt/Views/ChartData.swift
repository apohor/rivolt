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
    static func bands(drives: [DrivePoint], charges: [ChargePoint], from: Date, to: Date) -> [Band] {
        func make(_ kind: Band.Kind, _ s: Date, _ e: Date) -> Band? {
            let start = max(s, from), end = min(e, to)
            return end > start ? Band(kind: kind, start: start, end: end) : nil
        }
        let all = charges.compactMap { make(.charging, $0.startedAt, $0.endedAt) } + drives.compactMap { make(.driving, $0.startedAt, $0.endedAt) }
        return all.sorted { $0.start < $1.start }
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
