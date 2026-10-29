import Foundation
import Observation
import UserNotifications
#if canImport(UIKit)
import UIKit
#endif

/// How a notification is shown while the app is open.
enum NotificationPresentation {
    static let foreground: UNNotificationPresentationOptions = [.banner, .list, .sound]
}

/// The server calls push needs. `APIClient` is the real one; tests use a fake.
protocol PushServer: Sendable {
    func appConfig() async throws -> AppConfig
    func registerApns(token: String, environment: String) async throws -> PushPreferences
    func setApnsPreference(token: String, wireKey: String, on: Bool) async throws
    func unregisterApns(token: String) async throws
    func testApns() async throws -> Int
    func registerActivity(deviceToken: String, activityToken: String, vehicleID: String) async throws
    func endActivity(activityToken: String) async throws
    func testLiveActivity() async throws
}

extension APIClient: PushServer {}

enum PushAuthorization: Equatable {
    case notDetermined, denied, authorized
}

/// What the manager asks of iOS (permission and the Apple token), behind a
/// protocol so tests don't touch the real notification centre.
@MainActor
protocol NotificationSystem {
    func authorizationStatus() async -> PushAuthorization
    func requestAuthorization() async -> Bool
    func registerForRemoteNotifications()
    func unregisterForRemoteNotifications()
}

@MainActor
struct SystemNotifications: NotificationSystem {
    func authorizationStatus() async -> PushAuthorization {
        let s = await UNUserNotificationCenter.current().notificationSettings()
        switch s.authorizationStatus {
        case .authorized, .provisional, .ephemeral: return .authorized
        case .denied: return .denied
        default: return .notDetermined
        }
    }

    func requestAuthorization() async -> Bool {
        (try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])) ?? false
    }

    func registerForRemoteNotifications() {
        #if canImport(UIKit)
        UIApplication.shared.registerForRemoteNotifications()
        #endif
    }

    func unregisterForRemoteNotifications() {
        #if canImport(UIKit)
        UIApplication.shared.unregisterForRemoteNotifications()
        #endif
    }
}

/// Notifications on this iPhone: whether the server offers them, whether iOS
/// allows them, the Apple device token and the person's switches.
///
/// The rules (Sprint 6, slice 2):
///  - nothing is asked before sign-in, and nothing at all unless the server's
///    `/api/app/config` says `push.apns` is true;
///  - Rivolt's own card asks first; the iOS prompt only follows the person's tap;
///  - the Apple token goes to the server only while notifications are on, and is
///    removed again on sign-out or when the person turns them off.
@MainActor
@Observable
final class NotificationManager {
    static let shared = NotificationManager()

    /// Where the last tapped notification wants to go; the screen that handles it clears it. Set by the app delegate, read by AppShell
    /// (which may not exist yet when the tap launched the app).
    var pendingRoute: NotificationRoute?

    enum TestState: Equatable {
        case idle
        case sending
        case sent(Int)
        case failed(String)
    }

    enum Registration: Equatable {
        case idle
        case registering
        case registered
        case failed(String)
    }

    private(set) var offered = false
    /// The server sends charging Live Activity updates (`push.live`): the switch is only shown then.
    private(set) var liveOffered = false
    private(set) var authorization: PushAuthorization = .notDetermined
    private(set) var registration: Registration = .idle
    private(set) var deviceToken: String?
    private(set) var preferences: PushPreferences?
    /// The last "Send test notification", shown under the button.
    private(set) var test: TestState = .idle
    private(set) var promptDismissed: Bool
    /// The last failed switch, shown once under the list.
    private(set) var switchError: String?

    @ObservationIgnored private let system: NotificationSystem
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let environment: String
    @ObservationIgnored private var server: PushServer?
    @ObservationIgnored private var tokenWatchdog: Task<Void, Never>?

    private static let dismissedKey = "rivolt.push_prompt_dismissed"

    /// APNs has two environments; a debug build talks to the sandbox, anything
    /// shipped to TestFlight or the store to production.
    static var defaultEnvironment: String {
        #if DEBUG
        return "sandbox"
        #else
        return "production"
        #endif
    }

    /// The real one: iOS's notification centre and the standard defaults.
    convenience init() {
        self.init(system: SystemNotifications(), defaults: .standard, environment: Self.defaultEnvironment)
    }

    init(system: NotificationSystem, defaults: UserDefaults, environment: String) {
        self.system = system
        self.defaults = defaults
        self.environment = environment
        self.promptDismissed = defaults.bool(forKey: Self.dismissedKey)
    }

    /// Rivolt's own "turn on notifications" card: the server offers them, iOS
    /// hasn't been asked, and the person hasn't said "not now".
    var shouldAskInApp: Bool { offered && authorization == .notDetermined && !promptDismissed }

    /// Notifications are on when iOS allows them and the server has this phone.
    var isOn: Bool { authorization == .authorized && registration == .registered }

    // MARK: - Lifecycle

    /// Called once the person is signed in (cold launch with a valid token, or
    /// after sign-in). Reads what the server offers; if iOS already allows
    /// notifications, registers again, as Apple advises on every launch.
    func signedIn(server: PushServer) async {
        self.server = server
        let config = try? await server.appConfig()
        offered = config?.apnsAvailable ?? false
        liveOffered = config?.liveAvailable ?? false
        authorization = await system.authorizationStatus()
        guard offered else { return }
        if authorization == .authorized {
            if registration != .registered { registration = .registering }
            requestToken()
            if deviceToken != nil { await sync() }
        }
    }

    /// Sign-out: tell the server to forget this phone (best effort) and let iOS
    /// drop its token. The person's iOS permission stays as it was.
    func signedOut() async {
        if let server, let token = deviceToken {
            try? await server.unregisterApns(token: token)
        }
        system.unregisterForRemoteNotifications()
        server = nil
        offered = false
        liveOffered = false
        registration = .idle
        deviceToken = nil
        preferences = nil
        switchError = nil
        test = .idle
    }

    // MARK: - From the person

    /// "Turn on": the iOS permission prompt (once), then the Apple token.
    func turnOn() async {
        guard offered else { return }
        if authorization == .notDetermined {
            authorization = await system.requestAuthorization() ? .authorized : .denied
        }
        guard authorization == .authorized else { return }
        registration = .registering
        requestToken()
        if deviceToken != nil { await sync() }
    }

    /// "Not now" on the card. Remembered, so it isn't shown again; the switch in
    /// Settings still works.
    func dismissPrompt() {
        promptDismissed = true
        defaults.set(true, forKey: Self.dismissedKey)
    }

    /// "Turn off notifications on this iPhone".
    func turnOff() async {
        if let server, let token = deviceToken {
            try? await server.unregisterApns(token: token)
        }
        system.unregisterForRemoteNotifications()
        registration = .idle
        deviceToken = nil
        preferences = nil
        test = .idle
    }

    /// "Send test notification": the server sends one to this person's own iPhones. Only while
    /// notifications are on for this phone.
    func sendTest() async {
        guard isOn, let server else { return }
        test = .sending
        do {
            test = .sent(try await server.testApns())
        } catch {
            test = .failed(Self.testMessage(for: error))
        }
    }

    /// What to say when the test didn't go out.
    static func testMessage(for error: Error) -> String {
        if case APIError.server(let status, _) = error {
            if status == 404 { return "No iPhone to send to, or this server doesn't send notifications to the app." }
            if status == 429 { return "Too many tests. Wait a minute and try again." }
        }
        return "Couldn't send the test. Try again."
    }

    /// One switch. Shown at once, put back if the server says no.
    func set(_ kind: PushKind, to on: Bool) async {
        guard var prefs = preferences, let server, let token = deviceToken else { return }
        let before = prefs[kind]
        prefs[kind] = on
        preferences = prefs
        switchError = nil
        do {
            try await server.setApnsPreference(token: token, wireKey: kind.wireKey, on: on)
        } catch {
            var back = prefs
            back[kind] = before
            preferences = back
            switchError = "Couldn't change that switch. Try again."
        }
    }

    // MARK: - From iOS

    /// The Apple device token, as the hex string the server stores.
    func didRegister(deviceToken data: Data) {
        Task { await register(deviceToken: data) }
    }

    /// Keeps the token and tells the server (awaitable, for tests).
    func register(deviceToken data: Data) async {
        tokenWatchdog?.cancel()
        deviceToken = Self.hex(data)
        await sync()
    }

    func didFailToRegister(_ error: Error) {
        tokenWatchdog?.cancel()
        registration = .failed("iOS couldn't register this iPhone for notifications.")
    }

    private func sync() async {
        guard offered, authorization == .authorized, let server, let token = deviceToken else { return }
        // Registering again (iOS repeats the token callback) must not make an already-registered phone look
        // unregistered while it happens, or fail it when the server already has it.
        let wasRegistered = registration == .registered
        if !wasRegistered { registration = .registering }
        do {
            preferences = try await server.registerApns(token: token, environment: environment)
            registration = .registered
        } catch {
            if !wasRegistered { registration = .failed("Rivolt couldn't register this iPhone. Try again.") }
        }
    }

    /// Asks iOS for the Apple token, and says so if it never comes (no network, or the phone can't reach Apple),
    /// instead of leaving "Setting up…" on screen for good.
    private func requestToken() {
        system.registerForRemoteNotifications()
        tokenWatchdog?.cancel()
        tokenWatchdog = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 25_000_000_000)
            guard !Task.isCancelled, let self else { return }
            if self.deviceToken == nil, self.registration == .registering {
                self.registration = .failed("This iPhone didn't get a notification token from Apple. Check the connection and try again.")
            }
        }
    }

    static func hex(_ data: Data) -> String {
        data.map { String(format: "%02x", $0) }.joined()
    }
}

#if DEBUG
extension NotificationManager {
    /// A fixed state for simulator screenshots and previews: no network, no iOS prompts.
    static func preview(authorization: PushAuthorization, registered: Bool, preferences: PushPreferences? = nil, offered: Bool = true, test: TestState = .idle) -> NotificationManager {
        let m = NotificationManager(system: PreviewNotificationSystem(), defaults: UserDefaults(suiteName: "rivolt.preview") ?? .standard, environment: "sandbox")
        m.offered = offered
        m.authorization = authorization
        m.registration = registered ? Registration.registered : Registration.idle
        m.deviceToken = registered ? "preview" : nil
        m.preferences = registered ? (preferences ?? PushPreferences()) : nil
        m.promptDismissed = false
        m.test = test
        return m
    }
}

@MainActor
private struct PreviewNotificationSystem: NotificationSystem {
    func authorizationStatus() async -> PushAuthorization { .notDetermined }
    func requestAuthorization() async -> Bool { false }
    func registerForRemoteNotifications() {}
    func unregisterForRemoteNotifications() {}
}
#endif
