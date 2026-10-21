import ActivityKit
import Foundation
import Observation

/// What the manager needs of ActivityKit, behind a protocol so tests don't start real activities.
@MainActor
protocol LiveActivityBackend {
    /// Live Activities allowed for Rivolt in iOS Settings.
    var allowedInSettings: Bool { get }
    /// A Rivolt charging activity is on screen.
    var isRunning: Bool { get }
    /// Starts one (app in the foreground). `onToken` gets the activity's push token, hex, whenever iOS gives or changes it.
    func start(targetPct: Int?, isTest: Bool, state: ChargingActivityAttributes.ContentState, onToken: @escaping @MainActor (String) -> Void) throws
    /// Keeps hearing the token of an activity that survived an app restart.
    func observeTokens(onToken: @escaping @MainActor (String) -> Void)
    func update(state: ChargingActivityAttributes.ContentState) async
    func end() async
}

/// The charging Live Activity (Sprint 6 stretch). The app starts it while open and charging, and only when the
/// person has turned on the per-iPhone switch; the server then keeps it updated through Apple (charge level,
/// power and minutes to the limit only) and ends it with the charge. Read-only: it only displays.
@MainActor
@Observable
final class LiveActivityManager {
    static let shared = LiveActivityManager()

    /// How long the Lock Screen waits for an update before it calls the activity stale.
    static let staleAfter: TimeInterval = 15 * 60

    @ObservationIgnored private let backend: LiveActivityBackend
    @ObservationIgnored private var server: PushServer?
    @ObservationIgnored private var registeredToken: String?
    /// Which iPhone and truck the activity belongs to, for the server (set on each sync).
    @ObservationIgnored private var deviceToken: String?
    @ObservationIgnored private var vehicleID: String?
    @ObservationIgnored private var observing = false
    @ObservationIgnored private var registeredOK = false
    @ObservationIgnored private var testActive = false

    /// The last "Send test Live Activity", shown under the button.
    enum TestState: Equatable {
        case idle
        case working
        case sent
        case failed(String)
    }

    private(set) var test: TestState = .idle

    /// The fixed figures the server's test endpoint sends too (50%, 7.2 kW, 60 min), so the first card matches.
    static let testState = ChargingActivityAttributes.ContentState(socPct: 50, powerKw: 7.2, minutesToTarget: 60)

    convenience init() {
        self.init(backend: SystemLiveActivities())
    }

    init(backend: LiveActivityBackend) {
        self.backend = backend
    }

    /// Whether iOS lets Rivolt show Live Activities (for the hint under the switch).
    var allowedInSettings: Bool { backend.allowedInSettings }

    /// The server to tell about the activity's token (set after sign-in, cleared on sign-out).
    func attach(server: PushServer?) {
        self.server = server
    }

    /// Called after each refresh of the truck's state. `switchOn` is the person's per-iPhone switch.
    func sync(summary: NowSummary, switchOn: Bool, vehicleID: String?, deviceToken: String?) async {
        self.vehicleID = vehicleID
        self.deviceToken = deviceToken
        guard switchOn, backend.allowedInSettings, vehicleID != nil, deviceToken != nil else {
            await stop()
            return
        }
        // Data that has gone stale, or no data, changes nothing: the activity keeps what it has.
        guard summary.freshness == .fresh else { return }
        guard ChargingActivityMath.isCharging(summary), let state = ChargingActivityMath.state(from: summary) else {
            await stop()
            return
        }
        if backend.isRunning {
            if !observing { observe() }
            await backend.update(state: state)
        } else {
            do {
                try backend.start(targetPct: summary.chargeTargetPct.map { Int($0.rounded()) }, isTest: false, state: state, onToken: tokenHandler())
                observing = true
            } catch {
                // iOS refused (too many activities, not allowed right now): nothing to show, nothing to retry loudly.
            }
        }
    }

    /// Ends the activity on screen and tells the server (switch turned off, sign-out, charge over).
    func stop() async {
        if backend.isRunning { await backend.end() }
        observing = false
        registeredOK = false
        testActive = false
        if let token = registeredToken {
            registeredToken = nil
            try? await server?.endActivity(activityToken: token)
        }
    }

    /// "Send test Live Activity": starts a local activity with the fixed test figures (if none is running), waits for its
    /// token to be registered, then asks the server to push test figures and an end to it. For trying the whole path
    /// without a charge. A real charge in progress is left as it is.
    func sendTest(vehicleID: String?, deviceToken: String?) async {
        guard let server, backend.allowedInSettings, let vehicleID, let deviceToken else {
            test = .failed("Live Activities aren't available right now. Check the switch and iOS Settings.")
            return
        }
        test = .working
        self.vehicleID = vehicleID
        self.deviceToken = deviceToken
        if !backend.isRunning {
            do {
                try backend.start(targetPct: 80, isTest: true, state: Self.testState, onToken: tokenHandler())
                observing = true
                testActive = true
            } catch {
                test = .failed("iOS wouldn't start a Live Activity right now. Try again with the screen on.")
                return
            }
        } else if !observing {
            observe()
        }
        for _ in 0..<20 where !registeredOK {
            try? await Task.sleep(nanoseconds: 500_000_000)
        }
        guard registeredOK else {
            test = .failed("The server didn't take this iPhone's Live Activity. Check that the switch is on and try again.")
            return
        }
        do {
            try await server.testLiveActivity()
            test = .sent
            // The server ends it after about 20 seconds; if that push never arrives, end the test here.
            if testActive {
                Task { [weak self] in
                    try? await Task.sleep(nanoseconds: 90_000_000_000)
                    guard let self, self.testActive else { return }
                    await self.stop()
                }
            }
        } catch {
            test = .failed(Self.testMessage(for: error))
            if testActive { await stop() }
        }
    }

    /// What to say when the test didn't go out.
    static func testMessage(for error: Error) -> String {
        if case APIError.server(let status, _) = error {
            if status == 404 { return "Live Activities aren't set up on this server, or this activity wasn't registered." }
            if status == 409 { return "The Live Activity switch is off on the server." }
            if status == 429 { return "Too many tests. Wait a minute and try again." }
        }
        return "Couldn't send the test. Try again."
    }

    private func observe() {
        observing = true
        backend.observeTokens(onToken: tokenHandler())
    }

    private func tokenHandler() -> @MainActor (String) -> Void {
        { [weak self] token in
            guard let self else { return }
            self.registeredToken = token
            self.registeredOK = false
            guard let device = self.deviceToken, let vehicle = self.vehicleID else { return }
            // A 409 (the server's switch is off) or 404 is not retried: nothing is pushed for an unregistered activity.
            Task {
                do {
                    try await self.server?.registerActivity(deviceToken: device, activityToken: token, vehicleID: vehicle)
                    if self.registeredToken == token { self.registeredOK = true }
                } catch {
                    self.registeredOK = false
                }
            }
        }
    }
}

/// The real thing: ActivityKit.
@MainActor
final class SystemLiveActivities: LiveActivityBackend {
    private typealias A = Activity<ChargingActivityAttributes>

    var allowedInSettings: Bool { ActivityAuthorizationInfo().areActivitiesEnabled }

    var isRunning: Bool {
        A.activities.contains { $0.activityState == .active || $0.activityState == .stale }
    }

    func start(targetPct: Int?, isTest: Bool, state: ChargingActivityAttributes.ContentState, onToken: @escaping @MainActor (String) -> Void) throws {
        let content = ActivityContent(state: state, staleDate: Date().addingTimeInterval(LiveActivityManager.staleAfter))
        let activity = try A.request(attributes: ChargingActivityAttributes(targetPct: targetPct, isTest: isTest), content: content, pushType: .token)
        listen(activity, onToken)
    }

    func observeTokens(onToken: @escaping @MainActor (String) -> Void) {
        for activity in A.activities where activity.activityState == .active || activity.activityState == .stale {
            listen(activity, onToken)
        }
    }

    func update(state: ChargingActivityAttributes.ContentState) async {
        let content = ActivityContent(state: state, staleDate: Date().addingTimeInterval(LiveActivityManager.staleAfter))
        for activity in A.activities { await activity.update(content) }
    }

    func end() async {
        for activity in A.activities { await activity.end(nil, dismissalPolicy: .immediate) }
    }

    private func listen(_ activity: A, _ onToken: @escaping @MainActor (String) -> Void) {
        Task { @MainActor in
            for await data in activity.pushTokenUpdates {
                onToken(data.map { String(format: "%02x", $0) }.joined())
            }
        }
    }
}
