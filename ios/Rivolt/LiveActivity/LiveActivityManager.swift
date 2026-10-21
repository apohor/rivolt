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
    func start(targetPct: Int?, state: ChargingActivityAttributes.ContentState, onToken: @escaping @MainActor (String) -> Void) throws
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
                try backend.start(targetPct: summary.chargeTargetPct.map { Int($0.rounded()) }, state: state, onToken: tokenHandler())
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
        if let token = registeredToken {
            registeredToken = nil
            try? await server?.endActivity(activityToken: token)
        }
    }

    private func observe() {
        observing = true
        backend.observeTokens(onToken: tokenHandler())
    }

    private func tokenHandler() -> @MainActor (String) -> Void {
        { [weak self] token in
            guard let self else { return }
            self.registeredToken = token
            guard let device = self.deviceToken, let vehicle = self.vehicleID else { return }
            // A 409 (the server's switch is off) or 404 is not retried: nothing is pushed for an unregistered activity.
            Task { try? await self.server?.registerActivity(deviceToken: device, activityToken: token, vehicleID: vehicle) }
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

    func start(targetPct: Int?, state: ChargingActivityAttributes.ContentState, onToken: @escaping @MainActor (String) -> Void) throws {
        let content = ActivityContent(state: state, staleDate: Date().addingTimeInterval(LiveActivityManager.staleAfter))
        let activity = try A.request(attributes: ChargingActivityAttributes(targetPct: targetPct), content: content, pushType: .token)
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
