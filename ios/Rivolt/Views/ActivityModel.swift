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
    /// Why the last read failed, in words for the screen (never the server's own); nil when it did not fail or was just a 429.
    private(set) var problem: String?

    /// What kind of failure an error is, so the screen can tell "the server didn't answer" from "the answer didn't read".
    static func problem(for error: Error) -> String {
        switch error {
        case APIError.decoding:
            return "Rivolt couldn't read this answer. Updating the app may help."
        case APIError.server(let status, _) where (500...599).contains(status):
            return "Rivolt isn't answering right now. Pull to try again."
        case is URLError, APIError.transport:
            return "Couldn't reach Rivolt. Check the connection, then pull to try again."
        default:
            return "Couldn't load this period. Pull to try again."
        }
    }

    /// A dropped connection, a server error or "busy" (429) is worth one more try a moment later; a refusal or an unreadable answer is not.
    private static func isTransient(_ error: Error) -> Bool {
        switch error {
        case let e as URLError: return e.code != .cancelled
        case APIError.transport: return true
        case APIError.server(let status, _): return (500...599).contains(status) || status == 429
        default: return false
        }
    }

    private static func isBusy(_ error: Error) -> Bool {
        if case APIError.server(let status, _) = error { return status == 429 }
        return false
    }

    private struct Entry {
        let response: ActivityResponse
        let fetchedAt: Date
    }
    @ObservationIgnored private var cache: [String: Entry] = [:]
    /// Reads under way, by truck and month: a second ask for the same month (on appear, a refresh and a month change can land together)
    /// shares the first read instead of spending the server's per-minute allowance again.
    @ObservationIgnored private var inFlight: [String: Task<ActivityResponse, Error>] = [:]

    init(response: ActivityResponse? = nil) { self.response = response }

    func load(source: some ActivitySource, vehicleID: String, month: PeriodMonth, force: Bool = false,
              now: Date = .now, calendar: Calendar = .current, retryAfter: Duration = .seconds(2), busyRetryAfter: Duration = .seconds(3)) async {
        let key = "\(vehicleID)|\(month.key)"
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
            // One read per month at a time. The read itself is not tied to this caller, so a caller that is cancelled leaves it to finish
            // for the one that asked second.
            let read: Task<ActivityResponse, Error>
            if let running = inFlight[key] {
                read = running
            } else {
                read = Task {
                    do {
                        return try await source.activity(since: since, until: until, vehicleID: vehicleID)
                    } catch where Self.isTransient(error) {
                        // Busy (the server asked us to slow down) waits a little longer than a dropped connection.
                        try await Task.sleep(for: Self.isBusy(error) ? busyRetryAfter : retryAfter)
                        return try await source.activity(since: since, until: until, vehicleID: vehicleID)
                    }
                }
                inFlight[key] = read
            }
            defer { if inFlight[key] == read { inFlight[key] = nil } }
            let fresh = try await read.value
            cache[key] = Entry(response: fresh, fetchedAt: now)
            response = fresh
            failed = false
            limited = false
            problem = nil
        } catch {
            // A read that was cancelled (the screen moved on, a pull ended) is not a failure of the month.
            if Task.isCancelled || (error as? URLError)?.code == .cancelled || error is CancellationError { return }
            // Keep what is shown for the same truck and month; anything else would be another month's rows.
            response = cache[key]?.response
            failed = true
            if case APIError.server(let status, _) = error, status == 429 { limited = true; problem = nil } else { limited = false; problem = Self.problem(for: error) }
        }
    }
}
