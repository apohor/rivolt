import Foundation

/// The off-peak schedule's rules, apart from the view so a test can read them. A port of the web's `lib/offpeak.ts`: the same
/// day names, the same checks (the server refuses the same things with a 400) and the same hour strips.
enum OffpeakForm {
    static let maxWindows = 8
    static let rateMax = 5.0
    static let labelMax = 40

    enum Days: String, CaseIterable, Identifiable {
        case all, weekdays, weekends
        var id: String { rawValue }
        var title: String {
            switch self {
            case .all: return "Every day"
            case .weekdays: return "Weekdays"
            case .weekends: return "Weekends"
            }
        }
        fileprivate var indexes: [Int] {
            switch self {
            case .weekdays: return [0, 1, 2, 3, 4]
            case .weekends: return [5, 6]
            case .all: return [0, 1, 2, 3, 4, 5, 6]
            }
        }
    }

    /// A window as edited: the rate is text until it is valid.
    struct Draft: Equatable, Identifiable {
        let id: UUID
        var days: Days
        var start: String
        var end: String
        var rate: String
        var label: String

        init(id: UUID = UUID(), days: Days = .all, start: String = "21:00", end: String = "06:00", rate: String = "", label: String = "") {
            self.id = id; self.days = days; self.start = start; self.end = end; self.rate = rate; self.label = label
        }
    }

    struct WindowErrors: Equatable {
        var rate: String?
        var time: String?
        var label: String?
        var overlap: String?
        var any: Bool { rate != nil || time != nil || label != nil || overlap != nil }
    }

    /// Minutes since midnight for "HH:mm", or nil.
    static func minutes(_ t: String) -> Int? {
        let p = t.split(separator: ":", omittingEmptySubsequences: false)
        guard p.count == 2, p[0].count == 2, p[1].count == 2, let h = Int(p[0]), let m = Int(p[1]), h < 24, m < 60 else { return nil }
        return h * 60 + m
    }

    /// The wall clock is carried as a date on a fixed UTC day so a time picker shows exactly the hour and minute, whatever the phone's zone.
    static let utc: Calendar = {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC") ?? .gmt
        return c
    }()

    static func date(_ t: String) -> Date {
        let m = minutes(t) ?? 0
        return utc.date(from: DateComponents(year: 2001, month: 1, day: 1, hour: m / 60, minute: m % 60)) ?? Date(timeIntervalSinceReferenceDate: 0)
    }

    static func time(_ d: Date) -> String {
        let c = utc.dateComponents([.hour, .minute], from: d)
        return clock((c.hour ?? 0) * 60 + (c.minute ?? 0))
    }

    /// The zones the picker offers: the system's list, with the schedule's own first when the list lacks it.
    static func zones(including current: String) -> [String] {
        let all = TimeZone.knownTimeZoneIdentifiers.sorted()
        return current.isEmpty || all.contains(current) ? all : [current] + all
    }

    static func deviceZone() -> String { TimeZone.current.identifier }

    static func clock(_ minutes: Int) -> String { String(format: "%02d:%02d", minutes / 60, minutes % 60) }

    /// The week as [start, end) minute ranges, Monday 00:00 = 0. A window that ends after midnight continues into the next day, and
    /// the last day wraps into Monday.
    static func weekIntervals(days: Days, start: String, end: String) -> [(Int, Int)] {
        guard let s = minutes(start), let e = minutes(end), s != e else { return [] }
        let len = (e - s + 1440) % 1440
        var out: [(Int, Int)] = []
        for d in days.indexes {
            let from = d * 1440 + s
            let to = from + len
            if to <= 10080 {
                out.append((from, to))
            } else {
                out.append((from, 10080))
                out.append((0, to - 10080))
            }
        }
        return out
    }

    /// "9 PM", "6:30 AM".
    static func fmtClock(_ t: String) -> String {
        guard let m = minutes(t) else { return t }
        let h = m / 60, mi = m % 60
        let hour = h % 12 == 0 ? 12 : h % 12
        return "\(hour)\(mi > 0 ? String(format: ":%02d", mi) : "") \(h < 12 ? "AM" : "PM")"
    }

    static func describe(_ w: Draft) -> String { "\(w.days.title), \(fmtClock(w.start)) to \(fmtClock(w.end))" }

    static func validate(_ ws: [Draft]) -> [WindowErrors] {
        var errs = ws.map { w -> WindowErrors in
            var e = WindowErrors()
            let r = w.rate.trimmingCharacters(in: .whitespaces).replacingOccurrences(of: ",", with: ".")
            if r.isEmpty {
                e.rate = "Enter a rate."
            } else if let v = Double(r), v.isFinite, r.allSatisfy({ ($0 >= "0" && $0 <= "9") || $0 == "." }) {
                if v < 0 || v > rateMax {
                    e.rate = "The rate must be between 0 and \(Int(rateMax)) per kWh."
                } else if let dot = r.firstIndex(of: "."), r.distance(from: dot, to: r.endIndex) - 1 > 4 {
                    e.rate = "Use at most 4 decimal places."
                }
            } else {
                e.rate = "Enter a number."
            }
            if minutes(w.start) == nil || minutes(w.end) == nil {
                e.time = "Enter a start and an end time."
            } else if w.start == w.end {
                e.time = "The start and the end can't be the same time."
            }
            if w.label.count > labelMax { e.label = "The name can be at most \(labelMax) characters." }
            return e
        }
        let iv = ws.map { weekIntervals(days: $0.days, start: $0.start, end: $0.end) }
        if ws.count > 1 {
            for j in 1..<ws.count {
                for i in 0..<j {
                    let clash = iv[i].contains { a in iv[j].contains { c in a.0 < c.1 && c.0 < a.1 } }
                    if clash, errs[j].overlap == nil {
                        errs[j].overlap = "Overlaps window \(i + 1) (\(describe(ws[i]))). Change one of them."
                    }
                }
            }
        }
        return errs
    }

    static func hasErrors(_ errs: [WindowErrors]) -> Bool { errs.contains { $0.any } }

    enum Kind { case weekdays, weekends }

    /// Which hours of an ordinary weekday / weekend day the schedule covers (true when a window covers the whole hour).
    static func coverage(_ ws: [Draft], _ kind: Kind) -> [Bool] {
        let day = kind == .weekdays ? 1 : 5 // Tuesday / Saturday
        var out = [Bool](repeating: false, count: 24)
        for w in ws {
            for (a, b) in weekIntervals(days: w.days, start: w.start, end: w.end) {
                for h in 0..<24 {
                    let from = day * 1440 + h * 60
                    if a <= from && from + 60 <= b { out[h] = true }
                }
            }
        }
        return out
    }

    static func spokenCoverage(_ label: String, _ cov: [Bool]) -> String {
        let on = cov.filter { $0 }.count
        return "\(label): \(on == 0 ? "no off-peak hours" : "\(on) off-peak \(on == 1 ? "hour" : "hours") of 24")"
    }

    static func rateText(_ r: Double) -> String { ChargingRatesForm.fieldText(r) }

    static func drafts(_ s: OffpeakSchedule) -> [Draft] {
        s.windows.map {
            Draft(days: Days(rawValue: $0.days) ?? .all, start: $0.start, end: $0.end, rate: ChargingRatesForm.fieldText($0.rate), label: $0.label ?? "")
        }
    }

    /// The windows as the server takes them: rates to four places, an empty name left out.
    static func windows(_ ws: [Draft]) -> [OffpeakWindow] {
        ws.map { w in
            let r = Double(w.rate.trimmingCharacters(in: .whitespaces).replacingOccurrences(of: ",", with: ".")) ?? 0
            let name = w.label.trimmingCharacters(in: .whitespaces)
            return OffpeakWindow(days: w.days.rawValue, start: w.start, end: w.end, rate: (r * 10000).rounded() / 10000, label: name.isEmpty ? nil : name)
        }
    }

    /// Whether the drafts differ from what is saved (so Save is only live after a change).
    static func changed(_ ws: [Draft], tz: String, saved: OffpeakSchedule) -> Bool {
        tz != saved.tz || windows(ws) != saved.windows.map { var w = $0; if w.label?.isEmpty == true { w.label = nil }; return w }
    }

    static let intro = "If your utility charges less at certain hours, add those hours and their rate. You enter them yourself. Rivolt uses them only for estimates in Settings and Money; it does not contact your utility and cannot check them."
    static let flatTitle = "My electricity is one flat rate (no off-peak hours)"
    static func flatNote(_ on: Bool) -> String {
        on ? "Rivolt shows no off-peak savings, no off-peak line on share cards and no saving in your statement email. Your hours below are kept, and the estimates come back if you turn this off."
           : "Turn this on if your utility has no cheaper hours, so Rivolt stops offering off-peak estimates."
    }
    static let stripNote = "Midnight on the left. Green hours use an off-peak rate; the rest use your home rate."
}
