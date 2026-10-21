import Foundation

/// The period the Home charts cover: the last N local days, today included.
struct ChartWindow: Equatable, Hashable {
    let days: Int

    static let week = ChartWindow(days: 7)
    static let month = ChartWindow(days: 30)

    /// `since` is inclusive and `until` exclusive on the server: the start of the first local day and the start of the
    /// day after today.
    func range(now: Date, calendar: Calendar) -> (since: Date, until: Date) {
        let today = calendar.startOfDay(for: now)
        let since = calendar.date(byAdding: .day, value: -(days - 1), to: today) ?? today
        let until = calendar.date(byAdding: .day, value: 1, to: today) ?? today
        return (since, until)
    }
}

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
    /// Miles driven on each of the last `days` local days, bucketed by the local day each drive started.
    static func milesPerDay(_ drives: [DrivePoint], days: Int, now: Date, calendar: Calendar) -> [DayValue] {
        var buckets: [Date: Double] = [:]
        for d in drives {
            buckets[calendar.startOfDay(for: d.startedAt), default: 0] += max(0, d.distanceMi)
        }
        let today = calendar.startOfDay(for: now)
        return (0..<days).reversed().compactMap { back in
            guard let day = calendar.date(byAdding: .day, value: -back, to: today) else { return nil }
            return DayValue(day: day, value: buckets[day] ?? 0)
        }
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
}
