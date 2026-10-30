import Foundation
import Observation

/// Reads last Monday up to now once (the existing Activity answer, so no new endpoint) and keeps it for 15 minutes per truck. A failed
/// read leaves the strip as it was (hidden if there was none); a new truck never shows the last one's week.
@MainActor
@Observable
final class YourWeekModel {
    static let lifetime: TimeInterval = 15 * 60

    private(set) var week: YourWeekLogic.Week?
    @ObservationIgnored private var cache: [String: (week: YourWeekLogic.Week?, at: Date)] = [:]
    @ObservationIgnored private var shownKey: String?

    func load(source: some KpiSource, vehicleID: String, now: Date = .now, calendar: Calendar = .current, force: Bool = false) async {
        let key = "\(vehicleID)|\(YourWeekLogic.weekStart(now, calendar: calendar).timeIntervalSince1970)"
        if !force, let hit = cache[key], now.timeIntervalSince(hit.at) < Self.lifetime {
            week = hit.week; shownKey = key; return
        }
        if shownKey != key { week = nil; shownKey = key }
        let start = YourWeekLogic.weekStart(now, calendar: calendar)
        let since = calendar.date(byAdding: .day, value: -7, to: start) ?? start.addingTimeInterval(-7 * 86_400)
        do {
            let r = try await source.activity(since: since, until: now.addingTimeInterval(3600), vehicleID: vehicleID)
            if Task.isCancelled { return }
            let w = YourWeekLogic.week(drives: r.drives, charges: r.charges, now: now, calendar: calendar)
            cache[key] = (w, now)
            week = w
        } catch {
            // Keep what is shown; the strip is a convenience.
        }
    }
}
