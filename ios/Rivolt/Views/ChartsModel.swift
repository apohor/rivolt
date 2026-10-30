import Foundation
import Observation

/// The two reads behind the Home charts. Kept apart from `APIClient` so tests can count calls.
protocol ChartsSource: Sendable {
    func chartPoints(since: Date, until: Date, vehicleID: String) async throws -> ChartPoints
    func sleepActivity(since: Date, until: Date, vehicleID: String) async throws -> [DayActivity]
}

/// Loads the charts and keeps each answer in memory for 15 minutes per truck and period: the server groups up to 5,000
/// rows for every call, so Home refreshes don't repeat it. Pull-to-refresh forces a fresh read. A failed read leaves its
/// chart empty (the other still shows), as on the web.
@MainActor
@Observable
final class ChartsModel {
    static let lifetime: TimeInterval = 15 * 60

    private(set) var points: ChartPoints?
    private(set) var sleep: [DayActivity]?
    private(set) var isLoading = false

    private struct Entry {
        let points: ChartPoints?
        let sleep: [DayActivity]?
        let fetchedAt: Date
    }
    init(points: ChartPoints? = nil, sleep: [DayActivity]? = nil) {
        self.points = points
        self.sleep = sleep
    }

    @ObservationIgnored private var cache: [String: Entry] = [:]
    @ObservationIgnored private var shownKey: String?

    func load(source: some ChartsSource, vehicleID: String, month: PeriodMonth, force: Bool = false,
              now: Date = .now, calendar: Calendar = .current) async {
        let key = "\(vehicleID)|\(month.key)"
        if !force, let hit = cache[key], now.timeIntervalSince(hit.fetchedAt) < Self.lifetime {
            points = hit.points
            sleep = hit.sleep
            shownKey = key
            return
        }
        // Another month or truck: never leave the last one's chart under the new title while this one loads.
        if shownKey != key {
            points = nil
            sleep = nil
            shownKey = key
        }
        isLoading = true
        defer { isLoading = false }
        let (since, until) = month.range(calendar: calendar)
        async let p = try? source.chartPoints(since: since, until: until, vehicleID: vehicleID)
        async let s = try? source.sleepActivity(since: since, until: until, vehicleID: vehicleID)
        let (pp, ss) = await (p, s)
        // A load that was superseded (another period or truck on screen now) must not paint its answer over the current one.
        if shownKey == key {
            points = pp
            sleep = ss
        }
        // Only a complete answer is worth keeping; a failed half is retried on the next load.
        if pp != nil && ss != nil { cache[key] = Entry(points: pp, sleep: ss, fetchedAt: now) }
    }
}
