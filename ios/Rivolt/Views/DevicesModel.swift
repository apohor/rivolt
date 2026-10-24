import Foundation
import Observation

/// The two calls behind the devices screen, kept apart from `APIClient` so a test can see what is sent.
protocol DevicesSource: Sendable {
    func signedInDevices() async throws -> [SignedInDevice]
    func signOutDevice(id: String) async throws
}

extension APIClient: DevicesSource {}

/// The devices signed in to this account: read when the screen opens, and one device signed out on request. After a sign-out
/// the list is read again, so what shows is the server's own answer. A failure is said once, in plain words, and never retried
/// on its own.
@MainActor
@Observable
final class DevicesModel {
    enum LoadState: Equatable {
        case loading
        case loaded([SignedInDevice])
        case failed(String)
    }

    private(set) var loadState: LoadState = .loading
    /// The device being signed out right now (its row shows it).
    private(set) var signingOut: String?
    /// What went wrong signing a device out, or nil.
    private(set) var signOutError: String?

    func load(source: some DevicesSource) async {
        do {
            loadState = .loaded(Self.ordered(try await source.signedInDevices()))
        } catch {
            // With a list already shown, a failed refresh keeps it.
            if case .loaded = loadState { return }
            loadState = .failed(UserFacingError.words(error))
        }
    }

    func signOut(_ device: SignedInDevice, source: some DevicesSource) async {
        // This phone signs out through the account menu's Sign out, which also forgets its token: never from this list.
        guard !device.current, signingOut == nil else { return }
        signingOut = device.id
        signOutError = nil
        defer { signingOut = nil }
        do {
            try await source.signOutDevice(id: device.id)
        } catch {
            signOutError = UserFacingError.words(error)
            return
        }
        await load(source: source)
    }

    /// This device first, then the rest by when they were last seen, newest first.
    static func ordered(_ devices: [SignedInDevice]) -> [SignedInDevice] {
        devices.sorted { a, b in
            if a.current != b.current { return a.current }
            return (a.lastSeenAt ?? .distantPast) > (b.lastSeenAt ?? .distantPast)
        }
    }

    /// What a device is called: its own label as stored, else what kind of device it is.
    static func name(_ d: SignedInDevice) -> String {
        if let label = d.deviceLabel?.trimmingCharacters(in: .whitespacesAndNewlines), !label.isEmpty { return label }
        return d.kind == "app" ? "iPhone app" : "Web browser"
    }

    /// "iPhone app" or "Web browser", under the name.
    static func kindLabel(_ d: SignedInDevice) -> String { d.kind == "app" ? "iPhone app" : "Web browser" }

    /// "Signed in Oct 3 · Last active 2 hours ago", leaving out what the server didn't say.
    static func detail(_ d: SignedInDevice, now: Date = .now, calendar: Calendar = .current) -> String {
        var parts: [String] = []
        if let made = d.createdAt {
            let sameYear = calendar.isDate(made, equalTo: now, toGranularity: .year)
            parts.append("Signed in " + (sameYear ? made.formatted(.dateTime.month(.abbreviated).day()) : made.formatted(.dateTime.month(.abbreviated).day().year())))
        }
        if let seen = d.lastSeenAt {
            let age = max(0, now.timeIntervalSince(seen))
            parts.append(age < 60 ? "Active just now" : "Last active " + Self.relative(age))
        }
        return parts.joined(separator: " · ")
    }

    private static func relative(_ age: TimeInterval) -> String {
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = .full
        return f.localizedString(fromTimeInterval: -age)
    }
}
