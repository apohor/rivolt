import Foundation
import Observation

/// The one read behind the Activity screen. Kept apart from `APIClient` so tests can fake it.
protocol ActivitySource: Sendable {
    func activity(since: Date, until: Date, vehicleID: String) async throws -> ActivityResponse
}

extension APIClient: ActivitySource {}

/// Loads one month of one truck and keeps each answer in memory for 15 minutes: the server merges round trips and
/// classifies up to 5,000 charges for every call, and rate-limits it. Pull-to-refresh forces a read. A failed read
/// (including a 429) keeps what is on screen for the same truck and month and is not retried in a loop.
@MainActor
@Observable
final class ActivityModel {
    static let lifetime: TimeInterval = 15 * 60

    private(set) var response: ActivityResponse?
    private(set) var isLoading = false
    /// The last read failed; `limited` when the server said to slow down (429).
    private(set) var failed = false
    private(set) var limited = false

    private struct Entry {
        let response: ActivityResponse
        let fetchedAt: Date
    }
    @ObservationIgnored private var cache: [String: Entry] = [:]

    init(response: ActivityResponse? = nil) { self.response = response }

    func load(source: some ActivitySource, vehicleID: String, month: PeriodMonth, force: Bool = false,
              now: Date = .now, calendar: Calendar = .current) async {
        let key = "\(vehicleID)|\(month.start.timeIntervalSince1970)"
        if !force, let hit = cache[key], now.timeIntervalSince(hit.fetchedAt) < Self.lifetime {
            response = hit.response
            failed = false
            limited = false
            return
        }
        isLoading = true
        defer { isLoading = false }
        let (since, until) = month.range(calendar: calendar)
        do {
            let fresh = try await source.activity(since: since, until: until, vehicleID: vehicleID)
            cache[key] = Entry(response: fresh, fetchedAt: now)
            response = fresh
            failed = false
            limited = false
        } catch {
            // Keep what is shown for the same truck and month; anything else would be another month's rows.
            response = cache[key]?.response
            failed = true
            if case APIError.server(let status, _) = error, status == 429 { limited = true } else { limited = false }
        }
    }
}
