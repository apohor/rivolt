import Foundation

/// One calendar month on the phone, the period the Now page's stepper picks and its charts cover. `since` is inclusive
/// and `until` exclusive on the server, so a month is its first instant and the first instant of the next one.
struct PeriodMonth: Equatable, Hashable {
    /// The first instant of the month, in the phone's calendar.
    let start: Date

    init(containing date: Date, calendar: Calendar) {
        let c = calendar.dateComponents([.year, .month], from: date)
        start = calendar.date(from: c) ?? calendar.startOfDay(for: date)
    }

    func range(calendar: Calendar) -> (since: Date, until: Date) {
        (start, calendar.date(byAdding: .month, value: 1, to: start) ?? start)
    }

    func shifted(by months: Int, calendar: Calendar) -> PeriodMonth {
        PeriodMonth(containing: calendar.date(byAdding: .month, value: months, to: start) ?? start, calendar: calendar)
    }

    /// There is nothing to page forward to from the current month.
    func isCurrent(now: Date, calendar: Calendar) -> Bool {
        self == PeriodMonth(containing: now, calendar: calendar)
    }

    /// "Oct 2026", as the web's stepper shows it.
    func title(calendar: Calendar) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.abbreviated).year()
        f.calendar = calendar
        return start.formatted(f)
    }

    /// "October 2026", for speaking.
    func spokenTitle(calendar: Calendar) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.wide).year()
        f.calendar = calendar
        return start.formatted(f)
    }

    /// The days a per-day chart covers: the whole month, or in the current month through today.
    func days(now: Date, calendar: Calendar) -> [Date] {
        let (since, until) = range(calendar: calendar)
        let last = isCurrent(now: now, calendar: calendar) ? calendar.startOfDay(for: now) : until
        var out: [Date] = []
        var day = since
        while day < until, day <= last {
            out.append(day)
            guard let next = calendar.date(byAdding: .day, value: 1, to: day) else { break }
            day = next
        }
        return out
    }

    /// Every day of the month, 1st to last: the span both charts draw, so the current month's future days are empty
    /// room on the right and a past month fills the chart.
    func allDays(calendar: Calendar) -> [Date] {
        let (since, until) = range(calendar: calendar)
        var out: [Date] = []
        var day = since
        while day < until {
            out.append(day)
            guard let next = calendar.date(byAdding: .day, value: 1, to: day) else { break }
            day = next
        }
        return out
    }

    /// The axis labels both charts share: the 1st, 11th, 21st and the last day (10-01, 10-11, 10-21, 10-31).
    func axisDays(calendar: Calendar) -> [Date] {
        let all = allDays(calendar: calendar)
        guard let last = all.last else { return [] }
        return all.filter { [1, 11, 21].contains(calendar.component(.day, from: $0)) } + [last]
    }
}
