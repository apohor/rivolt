import Foundation
import os

/// How often the Now screen asks the server again, and when "Offline" may be said. A parked truck's newest frame ages on
/// the clock, so without a re-read the card walks to "Offline" while the server has a frame from a minute ago.
enum NowRefresh {
    /// Seconds between reads while the screen is up: quick while the numbers move (charging, driving), a minute when the
    /// truck is parked, plugged in or asleep.
    static func pollInterval(_ kind: NowSnapshot.State) -> TimeInterval {
        switch kind {
        case .charging, .driving: return 10
        case .plugged, .parked, .asleep, .unknown: return 60
        }
    }
}

/// When the screen last read the truck's state from the server. "Offline" is the truck's word, not ours: it is shown only
/// when the server was asked recently and still has nothing newer. If the last read is older than that (the app was in
/// the background, another tab was up), the card reads stale and the screen reads again first.
struct NowFreshness: Equatable {
    /// A read newer than this settles an "Offline"; the screen polls at least this often while it is up.
    static let confirmWindow: TimeInterval = 90

    private(set) var lastFetchAt: Date?

    mutating func fetched(at date: Date) { lastFetchAt = date }

    /// Whether to hold an "Offline" back until the server has been asked again.
    func holdsOffline(_ summary: NowSummary, clock: Date) -> Bool {
        guard summary.presence(holding: false) == .offline else { return false }
        guard let last = lastFetchAt else { return true }
        return clock.timeIntervalSince(last) > Self.confirmWindow
    }

    /// The summary as the card should show it.
    func shown(_ summary: NowSummary, clock: Date) -> NowSummary {
        var s = summary
        s.holdOffline = holdsOffline(summary, clock: clock)
        return s
    }
}

extension NowSummary {
    /// The presence with `holdOffline` set as asked, whatever the summary carries.
    func presence(holding: Bool) -> Presence {
        var s = self
        s.holdOffline = holding
        return s.presence
    }
}

/// The two reads behind the Now card, with the reason written to the log when one fails (a decoding error names the field;
/// it carries no coordinates or account data). A failed read leaves the card on what it already had.
enum NowReads {
    private static let log = Logger(subsystem: "dev.rivolt.app", category: "now")

    static func logged<T>(_ what: String, _ read: () async throws -> T) async -> T? {
        do {
            return try await read()
        } catch {
            log.error("\(what, privacy: .public) read failed: \(String(describing: error), privacy: .public)")
            return nil
        }
    }
}
