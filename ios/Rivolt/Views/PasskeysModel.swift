import Foundation
import Observation

/// The passkeys call, kept apart from `APIClient` so a test can see what is read.
protocol PasskeysSource: Sendable {
    func passkeys() async throws -> PasskeyList
}

extension APIClient: PasskeysSource {}

/// The account's passkeys under Signed-in devices. Read only: a passkey is added after asking for the password again, and removed,
/// on the web. A server with passkeys off answers 404; the section is then left out altogether.
@MainActor
@Observable
final class PasskeysModel {
    enum Phase: Equatable {
        case loading
        case loaded(PasskeyList)
        /// Passkeys are not turned on for this server (or this sign-in can't read them): nothing to show.
        case off
        case failed(String)
    }

    private(set) var phase = Phase.loading

    func load(source: some PasskeysSource) async {
        do {
            phase = .loaded(try await source.passkeys())
        } catch {
            if Task.isCancelled { return }
            if case APIError.server(let status, _) = error, [401, 403, 404].contains(status) {
                phase = .off
            } else if case .loaded = phase {
                return // a failed refresh keeps the list shown
            } else {
                phase = .failed("Couldn't load your passkeys. Try again in a minute.")
            }
        }
    }

    /// "Added Oct 28 · last used Nov 2", or "… · not used yet".
    static func detail(_ p: PasskeyEntry, now: Date = .now, calendar: Calendar = .current, timeZone: TimeZone = .current) -> String {
        func day(_ d: Date) -> String {
            let sameYear = calendar.isDate(d, equalTo: now, toGranularity: .year)
            let style = Date.FormatStyle(timeZone: timeZone).month(.abbreviated).day()
            return sameYear ? d.formatted(style) : d.formatted(style.year())
        }
        var parts: [String] = []
        if let made = p.createdAt { parts.append("Added \(day(made))") }
        parts.append(p.lastUsedAt.map { "last used \(day($0))" } ?? "not used yet")
        return parts.joined(separator: " · ")
    }

    /// Where it is kept, as the web says it: backed up to a keychain or password manager, or on this device alone.
    static func kind(_ p: PasskeyEntry) -> String { p.synced == true ? "Synced" : "This device" }
}
