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

/// The two reads behind the Now card, with the reason written to the log when one fails. What is logged is a fixed
/// class ("HTTP 503", "timed out", "no network", "unreadable: at") and, for a decoding error, only the field path: never
/// an error's own description, which can carry the request's URL, the server host or the underlying text.
enum NowReads {
    private static let log = Logger(subsystem: "dev.rivolt.app", category: "now")

    /// `record` gets the read's outcome as a fixed class (see `ConnectionDiagnostics.Outcome`) for the Connection details
    /// screen; it never sees the error.
    static func logged<T>(_ what: String, record: (@MainActor (ConnectionDiagnostics.Outcome) -> Void)? = nil,
                          _ read: () async throws -> T) async -> T? {
        do {
            let value = try await read()
            await record?(.ok)
            return value
        } catch {
            log.error("\(what, privacy: .public) read failed: \(describe(error), privacy: .public)")
            await record?(ConnectionDiagnostics.Outcome(error))
            return nil
        }
    }

    /// An error as one of a few fixed phrases, safe to log in the clear.
    static func describe(_ error: Error) -> String {
        switch error {
        case APIError.server(let status, _): return "HTTP \(status)"
        case APIError.decoding(let inner): return describe(inner)
        case APIError.badURL: return "bad URL"
        case APIError.transport: return "no answer"
        case let decoding as DecodingError: return "unreadable: \(decodingKind(decoding)) \(path(decoding))"
        case let url as URLError:
            switch url.code {
            case .timedOut: return "timed out"
            case .notConnectedToInternet, .networkConnectionLost, .dataNotAllowed, .internationalRoamingOff: return "no network"
            case .cannotConnectToHost, .cannotFindHost, .dnsLookupFailed, .secureConnectionFailed, .serverCertificateUntrusted: return "can't reach the server"
            case .cancelled: return "cancelled"
            default: return "network error"
            }
        default: return "failed"
        }
    }

    private static func decodingKind(_ e: DecodingError) -> String {
        switch e {
        case .keyNotFound: return "missing"
        case .typeMismatch: return "wrong type"
        case .valueNotFound: return "empty"
        case .dataCorrupted: return "corrupt"
        @unknown default: return "unknown"
        }
    }

    /// The coding path of a decoding error ("at", "charge.startedAt"): the app's own field names, no values.
    static func path(_ e: DecodingError) -> String {
        let keys: [CodingKey]
        switch e {
        case .keyNotFound(let key, let ctx): keys = ctx.codingPath + [key]
        case .typeMismatch(_, let ctx), .valueNotFound(_, let ctx), .dataCorrupted(let ctx): keys = ctx.codingPath
        @unknown default: keys = []
        }
        let joined = keys.map(\.stringValue).joined(separator: ".")
        return joined.isEmpty ? "(root)" : joined
    }
}
