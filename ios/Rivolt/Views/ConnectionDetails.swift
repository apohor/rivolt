import Foundation
import Observation
import SwiftUI

/// What the Connection details screen knows, kept in memory only: the outcome of the Now screen's last two reads and the
/// summary of the newest frame. It exists to answer "why does the truck say Offline?" (the server answered or didn't; the
/// newest frame is this old; the limit for what the truck is doing is that). Nothing here is stored, logged or sent, and
/// nothing identifies the account or the truck: an outcome is one of a few fixed classes, never an error's own text (which
/// can carry the request's URL).
@MainActor
@Observable
final class ConnectionDiagnostics {
    enum Kind { case now, state }

    /// How a read ended, as a fixed class.
    enum Outcome: Equatable {
        case ok
        case http(Int)
        case timedOut
        case noNetwork
        case unreachable
        case unreadable
        case failed

        init(_ error: Error) {
            switch error {
            case APIError.server(let status, _): self = .http(status)
            case APIError.decoding, is DecodingError: self = .unreadable
            case APIError.badURL, APIError.transport: self = .unreachable
            case let url as URLError:
                switch url.code {
                case .timedOut: self = .timedOut
                case .notConnectedToInternet, .networkConnectionLost, .dataNotAllowed, .internationalRoamingOff: self = .noNetwork
                case .cannotConnectToHost, .cannotFindHost, .dnsLookupFailed, .secureConnectionFailed, .serverCertificateUntrusted: self = .unreachable
                default: self = .failed
                }
            default: self = .failed
            }
        }

        var text: String {
            switch self {
            case .ok: return "OK"
            case .http(let status): return "HTTP \(status)"
            case .timedOut: return "Timed out"
            case .noNetwork: return "No network"
            case .unreachable: return "Can't reach the server"
            case .unreadable: return "Unreadable answer"
            case .failed: return "Failed"
            }
        }
    }

    struct Read: Equatable {
        let at: Date
        let outcome: Outcome
    }

    private(set) var now: Read?
    private(set) var state: Read?
    /// The Now card's summary (the newest frame and what the truck is doing) and when the screen last got an answer.
    private(set) var summary: NowSummary?
    private(set) var freshness = NowFreshness()

    func record(_ kind: Kind, _ outcome: Outcome, at date: Date = .now) {
        let read = Read(at: date, outcome: outcome)
        switch kind {
        case .now: now = read
        case .state: state = read
        }
    }

    func update(summary: NowSummary, freshness: NowFreshness) {
        self.summary = summary
        self.freshness = freshness
    }
}

/// The screen's wording, kept apart from the view so it can be tested.
enum ConnectionDetails {
    /// "rivolt.dev" or "rivolt.dev:8443": the host and the port, never a user, a password, a path or a query.
    static func host(of url: URL) -> String {
        guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false), let host = c.host, !host.isEmpty else { return "Unknown" }
        return c.port.map { "\(host):\($0)" } ?? host
    }

    /// "12 s ago", "3 min ago", "2 h 5 min ago".
    static func ago(_ date: Date, clock: Date) -> String {
        let s = max(0, Int(clock.timeIntervalSince(date)))
        if s < 60 { return "\(s) s ago" }
        if s < 3600 { return "\(s / 60) min ago" }
        let m = (s % 3600) / 60
        return m == 0 ? "\(s / 3600) h ago" : "\(s / 3600) h \(m) min ago"
    }

    /// "30 s", "3 min", "45 min".
    static func span(_ seconds: TimeInterval) -> String {
        let s = Int(seconds)
        return s < 60 || s % 60 != 0 ? "\(s) s" : "\(s / 60) min"
    }

    static func presenceText(_ p: NowSummary.Presence) -> String {
        switch p {
        case .live: return "Live"
        case .stale: return "Stale"
        case .offline: return "Offline"
        }
    }

    static func appVersion(bundle: Bundle = .main) -> String {
        let version = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
        let build = bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "?"
        return "\(version) (\(build))"
    }

    /// One read's line: "OK, 12 s ago", "HTTP 503, 3 min ago", or "Not read yet".
    static func readLine(_ read: ConnectionDiagnostics.Read?, clock: Date) -> String {
        guard let read else { return "Not read yet" }
        return "\(read.outcome.text), \(ago(read.at, clock: clock))"
    }
}

/// The account menu's "Connection details": read-only, in memory, nothing to copy or send. For the owner's "the truck says
/// Offline": it shows whether the server answered, how old the newest frame is, and the limit that makes it Offline.
struct ConnectionDetailsView: View {
    let diagnostics: ConnectionDiagnostics
    let serverURL: URL
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            TimelineView(.periodic(from: .now, by: 1)) { context in
                ScrollView {
                    ConnectionDetailsContent(diagnostics: diagnostics, serverURL: serverURL, clock: context.date)
                        .padding(4 * Theme.unit)
                }
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .navigationTitle("Connection details")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
        }
    }
}

/// The rows, drawn at a given clock (the sheet ticks it every second; tests and renders pin it).
struct ConnectionDetailsContent: View {
    let diagnostics: ConnectionDiagnostics
    let serverURL: URL
    let clock: Date

    var body: some View {
        VStack(spacing: 4 * Theme.unit) {
            ThemeSection(title: "This app") {
                row("Server", ConnectionDetails.host(of: serverURL))
                row("App", ConnectionDetails.appVersion())
            }
            ThemeSection(title: "Last reads") {
                row("Truck now", ConnectionDetails.readLine(diagnostics.now, clock: clock))
                row("Truck state", ConnectionDetails.readLine(diagnostics.state, clock: clock))
            }
            presence
        }
    }

    @ViewBuilder
    private var presence: some View {
        ThemeSection(title: "Newest frame") {
            if let summary = diagnostics.summary {
                let held = diagnostics.freshness.holdsOffline(summary, clock: clock)
                let limits = summary.limits
                row("Newest frame", summary.lastSeen.map { ConnectionDetails.ago($0, clock: clock) } ?? "None yet")
                if let seen = summary.lastSeen {
                    row("At", seen.formatted(date: .omitted, time: .standard))
                }
                row("Shown as", ConnectionDetails.presenceText(summary.presence(holding: held)))
                row("Stale after", ConnectionDetails.span(limits.stale))
                row("Offline after", ConnectionDetails.span(limits.offline))
                if held {
                    Text("Offline is held back until the server has been asked again.").themeText(.subline)
                        .fixedSize(horizontal: false, vertical: true)
                }
            } else {
                Text("The truck hasn't been read yet.").themeText(.value)
            }
        }
    }

    private func row(_ label: String, _ value: String) -> some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label).themeText(.value, color: Theme.textSecondary)
            Spacer(minLength: 12)
            Text(verbatim: value).themeText(.value, color: Theme.textPrimary).multilineTextAlignment(.trailing)
        }
        .accessibilityElement(children: .combine)
    }
}
