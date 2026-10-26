import Foundation

/// The span of time the Now, Activity and Truck screens cover, as the web's period picker: Day, Week (Monday to Sunday), Month or
/// a Custom range, with two shortcuts inside Custom, This year and All time. Money steps by month only (`PeriodMonth`, a month
/// `Period`). `start` is inclusive and `end` exclusive, as the server takes `since` and `until`: a day is its midnight to the next one.
/// The kind and how far back it was stepped are remembered (`Stored`), so "Today" is still today tomorrow.
struct Period: Equatable, Hashable {
    enum Kind: String, Codable, CaseIterable { case day, week, month, year, all, custom }

    let kind: Kind
    /// The first instant, in the phone's calendar.
    let start: Date
    /// The first instant after it.
    let end: Date

    private init(kind: Kind, start: Date, end: Date) {
        self.kind = kind; self.start = start; self.end = end
    }

    /// The month containing `date` (Money's period, and the screens' old one).
    init(containing date: Date, calendar: Calendar) { self.init(kind: .month, containing: date, calendar: calendar) }

    /// The day, week, month or year containing `date`. (Custom and All time have their own makers.)
    init(kind: Kind, containing date: Date, calendar: Calendar) {
        let day = calendar.startOfDay(for: date)
        func add(_ c: Calendar.Component, _ n: Int, to d: Date) -> Date { calendar.date(byAdding: c, value: n, to: d) ?? d }
        switch kind {
        case .day, .custom, .all:
            // Custom and All time have their own makers; asked for here they are the day.
            self.init(kind: .day, start: day, end: add(.day, 1, to: day))
        case .week:
            // Monday to Sunday, like the web's, whatever the phone's first weekday.
            let sinceMonday = (calendar.component(.weekday, from: day) + 5) % 7
            let monday = add(.day, -sinceMonday, to: day)
            self.init(kind: .week, start: monday, end: add(.day, 7, to: monday))
        case .month:
            let c = calendar.dateComponents([.year, .month], from: date)
            let s = calendar.date(from: c) ?? day
            self.init(kind: .month, start: s, end: add(.month, 1, to: s))
        case .year:
            let c = calendar.dateComponents([.year], from: date)
            let s = calendar.date(from: c) ?? day
            self.init(kind: .year, start: s, end: add(.year, 1, to: s))
        }
    }

    /// The days from `from` to `to`, both included (swapped ends are put right).
    static func custom(from: Date, to: Date, calendar: Calendar) -> Period {
        let a = calendar.startOfDay(for: min(from, to)), b = calendar.startOfDay(for: max(from, to))
        return Period(kind: .custom, start: a, end: calendar.date(byAdding: .day, value: 1, to: b) ?? b)
    }

    /// Everything the truck has recorded: from before the first Rivian reached a driveway to the end of today.
    static func allTime(now: Date, calendar: Calendar) -> Period {
        let first = calendar.date(from: DateComponents(year: 2020, month: 1, day: 1)) ?? .distantPast
        let tomorrow = calendar.date(byAdding: .day, value: 1, to: calendar.startOfDay(for: now)) ?? now
        return Period(kind: .all, start: first, end: tomorrow)
    }

    // MARK: Range and stepping

    func range(calendar: Calendar) -> (since: Date, until: Date) { (start, end) }

    /// Day, week, month and year step; Custom and All time do not.
    var canStep: Bool { kind == .day || kind == .week || kind == .month || kind == .year }

    func shifted(by n: Int, calendar: Calendar) -> Period {
        guard canStep else { return self }
        let unit: Calendar.Component
        switch kind {
        case .day: unit = .day
        case .week: unit = .weekOfYear
        case .month: unit = .month
        default: unit = .year
        }
        return Period(kind: kind, containing: calendar.date(byAdding: unit, value: n, to: start) ?? start, calendar: calendar)
    }

    /// There is nothing to page forward to from the period that holds now.
    func isCurrent(now: Date, calendar: Calendar) -> Bool { end > now }

    /// A key that tells one period from another for caches: a day and a month starting the same instant are different reads.
    var key: String { "\(kind.rawValue)|\(start.timeIntervalSince1970)|\(end.timeIntervalSince1970)" }

    // MARK: Words (the web's, character for character)

    private static let months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]

    private func md(_ d: Date, _ calendar: Calendar) -> String {
        "\(Self.months[calendar.component(.month, from: d) - 1]) \(calendar.component(.day, from: d))"
    }

    private func lastDay(_ calendar: Calendar) -> Date { calendar.date(byAdding: .day, value: -1, to: end) ?? end }

    /// The picker's label: "Today", "Oct 24", "Oct 6–12", "Sep 29–Oct 5", "Oct 2026", "2026", "All time", "Oct 3 – Oct 17, 2026".
    func title(calendar: Calendar, now: Date = .now) -> String {
        switch kind {
        case .all:
            return "All time"
        case .day:
            if calendar.isDate(start, inSameDayAs: now) { return "Today" }
            let sameYear = calendar.component(.year, from: start) == calendar.component(.year, from: now)
            return sameYear ? md(start, calendar) : "\(md(start, calendar)), \(calendar.component(.year, from: start))"
        case .week:
            let last = lastDay(calendar)
            if calendar.component(.month, from: start) == calendar.component(.month, from: last) {
                return "\(md(start, calendar))–\(calendar.component(.day, from: last))"
            }
            return "\(md(start, calendar))–\(md(last, calendar))"
        case .month:
            var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.abbreviated).year()
            f.calendar = calendar
            return start.formatted(f)
        case .year:
            return String(calendar.component(.year, from: start))
        case .custom:
            let last = lastDay(calendar)
            let sameYear = calendar.component(.year, from: start) == calendar.component(.year, from: last)
            let a = sameYear ? md(start, calendar) : "\(md(start, calendar)), \(calendar.component(.year, from: start))"
            return "\(a) – \(md(last, calendar)), \(calendar.component(.year, from: last))"
        }
    }

    /// What VoiceOver says: "October 2026", "today", "October 24", "the week of October 6", "2026", "all time", "October 3 to October 17, 2026".
    func spokenTitle(calendar: Calendar, now: Date = .now) -> String {
        func wide(_ d: Date, year: Bool) -> String {
            var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.wide).day()
            if year { f = f.year() }
            f.calendar = calendar
            return d.formatted(f)
        }
        switch kind {
        case .all: return "all time"
        case .day: return calendar.isDate(start, inSameDayAs: now) ? "today" : wide(start, year: calendar.component(.year, from: start) != calendar.component(.year, from: now))
        case .week: return "the week of \(wide(start, year: false))"
        case .month:
            var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.wide).year()
            f.calendar = calendar
            return start.formatted(f)
        case .year: return String(calendar.component(.year, from: start))
        case .custom: return "\(wide(start, year: false)) to \(wide(lastDay(calendar), year: true))"
        }
    }

    /// A phrase for "No drives or charges ___.": "in October 2026", "today", "on Oct 24", "in the week of October 6", "in 2026", "so far",
    /// "from Oct 3 to Oct 17".
    func inPhrase(calendar: Calendar, now: Date = .now) -> String {
        switch kind {
        case .all: return "so far"
        case .day: return calendar.isDate(start, inSameDayAs: now) ? "today" : "on \(spokenTitle(calendar: calendar, now: now))"
        case .custom: return "from \(spokenTitle(calendar: calendar, now: now))"
        default: return "in \(spokenTitle(calendar: calendar, now: now))"
        }
    }

    // MARK: Days (for the per-day charts)

    /// The days a per-day chart covers: the whole period, or through today when it holds today.
    func days(now: Date, calendar: Calendar) -> [Date] {
        let last = isCurrent(now: now, calendar: calendar) ? calendar.startOfDay(for: now) : end
        var out: [Date] = []
        var day = start
        while day < end, day <= last {
            out.append(day)
            guard let next = calendar.date(byAdding: .day, value: 1, to: day) else { break }
            day = next
        }
        return out
    }

    /// Every day of the period, first to last: the span the charts draw, so a current month's future days are empty room on the right.
    func allDays(calendar: Calendar) -> [Date] {
        var out: [Date] = []
        var day = start
        while day < end {
            out.append(day)
            guard let next = calendar.date(byAdding: .day, value: 1, to: day) else { break }
            day = next
        }
        return out
    }

    /// The axis labels the charts share, as the web's `axisDayIndices`: every day up to a week, the 1st, 11th, 21st and the last day up to
    /// a month, and four evenly spaced days beyond.
    func axisDays(calendar: Calendar) -> [Date] {
        let all = allDays(calendar: calendar)
        guard let last = all.last else { return [] }
        if all.count <= 8 { return all }
        if all.count <= 32 { return all.filter { [1, 11, 21].contains(calendar.component(.day, from: $0)) } + [last] }
        return (0..<4).map { all[min(all.count - 1, $0 * (all.count - 1) / 3)] }
    }

    // MARK: Remembering the choice

    /// What is kept on the phone: the kind and how many periods back (so "Today" and "This month" follow the calendar), or a custom
    /// range's days. Year and All time are kept as they are, and a saved choice that can't be read falls back to this month.
    struct Stored: Codable, Equatable {
        var kind: Kind
        var offset: Int = 0
        var from: String?
        var to: String?
    }

    private static func ymd(_ d: Date, _ calendar: Calendar) -> String {
        let c = calendar.dateComponents([.year, .month, .day], from: d)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    private static func parse(_ s: String?, _ calendar: Calendar) -> Date? {
        guard let s, s.count == 10 else { return nil }
        let p = s.split(separator: "-").compactMap { Int($0) }
        guard p.count == 3 else { return nil }
        return calendar.date(from: DateComponents(year: p[0], month: p[1], day: p[2]))
    }

    func stored(now: Date, calendar: Calendar) -> Stored {
        switch kind {
        case .all: return Stored(kind: .all)
        case .custom: return Stored(kind: .custom, from: Self.ymd(start, calendar), to: Self.ymd(lastDay(calendar), calendar))
        default:
            let current = Period(kind: kind, containing: now, calendar: calendar)
            var n = 0
            var p = current
            // Walk back to this period: periods are few, and the walk is exact across month lengths and DST.
            while p.start > start, n < 5000 { p = p.shifted(by: -1, calendar: calendar); n += 1 }
            return Stored(kind: kind, offset: -n)
        }
    }

    init(_ s: Stored, now: Date, calendar: Calendar) {
        switch s.kind {
        case .all:
            self = Period.allTime(now: now, calendar: calendar)
        case .custom:
            if let a = Self.parse(s.from, calendar), let b = Self.parse(s.to, calendar) {
                self = Period.custom(from: a, to: b, calendar: calendar)
            } else {
                self = Period(kind: .month, containing: now, calendar: calendar)
            }
        default:
            let current = Period(kind: s.kind, containing: now, calendar: calendar)
            self = current.shifted(by: min(0, s.offset), calendar: calendar)
        }
    }
}

/// Money's period: always one calendar month.
typealias PeriodMonth = Period
