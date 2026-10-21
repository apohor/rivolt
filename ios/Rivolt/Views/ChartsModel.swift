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

    func load(source: some ChartsSource, vehicleID: String, window: ChartWindow, force: Bool = false,
              now: Date = .now, calendar: Calendar = .current) async {
        let key = "\(vehicleID)|\(window.days)"
        if !force, let hit = cache[key], now.timeIntervalSince(hit.fetchedAt) < Self.lifetime {
            points = hit.points
            sleep = hit.sleep
            return
        }
        isLoading = true
        defer { isLoading = false }
        let (since, until) = window.range(now: now, calendar: calendar)
        async let p = try? source.chartPoints(since: since, until: until, vehicleID: vehicleID)
        async let s = try? source.sleepActivity(since: since, until: until, vehicleID: vehicleID)
        let (pp, ss) = await (p, s)
        points = pp
        sleep = ss
        // Only a read that worked is worth keeping; a failure is retried on the next load.
        if pp != nil || ss != nil { cache[key] = Entry(points: pp, sleep: ss, fetchedAt: now) }
    }
}
