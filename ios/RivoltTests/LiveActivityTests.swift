import SwiftUI
import XCTest
@testable import Rivolt

private final class LiveFakeServer: PushServer, @unchecked Sendable {
    var registered: [String] = []
    var ended: [String] = []

    func appConfig() async throws -> AppConfig { AppConfig(server: "rivolt", apiVersion: 1, appLogin: true, minIosBuild: nil, push: .init(apns: true)) }
    func registerApns(token: String, environment: String) async throws -> PushPreferences { PushPreferences() }
    func setApnsPreference(token: String, wireKey: String, on: Bool) async throws {}
    func unregisterApns(token: String) async throws {}
    func testApns() async throws -> Int { 1 }
    var registeredWith: [(device: String, vehicle: String)] = []
    func registerActivity(deviceToken: String, activityToken: String, vehicleID: String) async throws {
        registered.append(activityToken)
        registeredWith.append((deviceToken, vehicleID))
    }
    func endActivity(activityToken: String) async throws { ended.append(activityToken) }
    var liveTests = 0
    var liveTestResult: Error?
    func testLiveActivity() async throws {
        liveTests += 1
        if let e = liveTestResult { throw e }
    }
}

@MainActor
private final class FakeBackend: LiveActivityBackend {
    var allowedInSettings = true
    var isRunning = false
    var started: [(target: Int?, isTest: Bool, state: ChargingActivityAttributes.ContentState)] = []
    var updates: [ChargingActivityAttributes.ContentState] = []
    var endCalls = 0
    var onToken: (@MainActor (String) -> Void)?

    func start(targetPct: Int?, isTest: Bool, state: ChargingActivityAttributes.ContentState, onToken: @escaping @MainActor (String) -> Void) throws {
        started.append((targetPct, isTest, state))
        isRunning = true
        self.onToken = onToken
    }

    func observeTokens(onToken: @escaping @MainActor (String) -> Void) { self.onToken = onToken }
    func update(state: ChargingActivityAttributes.ContentState) async { updates.append(state) }
    func end() async {
        endCalls += 1
        isRunning = false
    }
}

final class ChargingActivityContentTests: XCTestCase {
    /// The app's one rounding rule (NumberText): a half rounds away from zero, as the web's toFixed, never to even.
    func testPowerRoundsAsTheWebDoes() {
        let s = ChargingActivityAttributes.ContentState(socPct: 50, powerKw: 7.25, minutesToTarget: nil)
        XCTAssertEqual(ChargingActivityText.power(s), "7.3 kW")
        XCTAssertTrue(ChargingActivityText.spoken(s, targetPct: nil, isStale: false).contains("7.3 kilowatts"))
    }

    /// ActivityKit decodes a push's content-state with a plain JSONDecoder.
    func testDecodesExactlyTheServersKeys() throws {
        let json = #"{"soc_pct":63,"power_kw":11.1,"minutes_to_target":42}"#
        let s = try JSONDecoder().decode(ChargingActivityAttributes.ContentState.self, from: Data(json.utf8))
        XCTAssertEqual(s, .init(socPct: 63, powerKw: 11.1, minutesToTarget: 42))
    }

    func testMinutesMayBeNullOrAbsentAndExtraKeysAreIgnored() throws {
        let nullMin = try JSONDecoder().decode(ChargingActivityAttributes.ContentState.self, from: Data(#"{"soc_pct":80,"power_kw":0.0,"minutes_to_target":null}"#.utf8))
        XCTAssertNil(nullMin.minutesToTarget)
        let absent = try JSONDecoder().decode(ChargingActivityAttributes.ContentState.self, from: Data(#"{"soc_pct":80,"power_kw":0.0,"extra":"x"}"#.utf8))
        XCTAssertNil(absent.minutesToTarget)
    }

    func testEncodesOnlyTheThreeFields() throws {
        let data = try JSONEncoder().encode(ChargingActivityAttributes.ContentState(socPct: 63, powerKw: 11.1, minutesToTarget: 42))
        let keys = Set(try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any]).keys)
        XCTAssertEqual(keys, ["soc_pct", "power_kw", "minutes_to_target"])
    }

    func testMinutesToTargetNeedsEverythingAndIsNeverAGuess() {
        XCTAssertEqual(ChargingActivityMath.minutesToTarget(socPct: 63.4, targetPct: 80, packKwh: 135, powerKw: 11.1), 122)
        XCTAssertNil(ChargingActivityMath.minutesToTarget(socPct: 63.4, targetPct: nil, packKwh: 135, powerKw: 11.1))
        XCTAssertNil(ChargingActivityMath.minutesToTarget(socPct: 63.4, targetPct: 80, packKwh: nil, powerKw: 11.1))
        XCTAssertNil(ChargingActivityMath.minutesToTarget(socPct: 63.4, targetPct: 80, packKwh: 135, powerKw: nil))
        XCTAssertNil(ChargingActivityMath.minutesToTarget(socPct: 63.4, targetPct: 80, packKwh: 135, powerKw: 0.05))
        XCTAssertNil(ChargingActivityMath.minutesToTarget(socPct: 80, targetPct: 80, packKwh: 135, powerKw: 11))
    }

    func testTheWording() {
        XCTAssertEqual(ChargingActivityText.untilTarget(42, targetPct: 80), "about 42 min to 80%")
        XCTAssertEqual(ChargingActivityText.untilTarget(122, targetPct: 80), "about 2 h 2 min to 80%")
        XCTAssertEqual(ChargingActivityText.untilTarget(30, targetPct: nil), "about 30 min")
        XCTAssertNil(ChargingActivityText.untilTarget(nil, targetPct: 80))
        XCTAssertNil(ChargingActivityText.untilTarget(0, targetPct: 80), "the server's 0 means unknown")
    }

    func testTheSwitchIsOffByDefaultAndHiddenWhenTheServerDoesNotKnowIt() throws {
        XCTAssertFalse(PushPreferences().onChargingLive)
        // The API client's decoder converts snake_case keys.
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        let without = try d.decode(PushPreferences.self, from: Data(#"{"on_charging_done":true}"#.utf8))
        XCTAssertFalse(without.chargingLiveKnown)
        XCTAssertFalse(without.onChargingLive)
        let with = try d.decode(PushPreferences.self, from: Data(#"{"on_charging_live":true}"#.utf8))
        XCTAssertTrue(with.chargingLiveKnown)
        XCTAssertTrue(with.onChargingLive)
        XCTAssertEqual(PushKind.chargingLive.wireKey, "on_charging_live")
        XCTAssertTrue(PushKind.chargingLive.help?.contains("Apple's push service gets your charge level") == true)
    }
}

@MainActor
final class LiveActivityManagerTests: XCTestCase {
    private func charging(clock: TimeInterval = 60) throws -> NowSummary {
        let st = try Fixture.decode(VehicleState.self, "state")
        let now = try Fixture.decode(NowSnapshot.self, "now_v1")
        return NowSummary(state: st, now: now, clock: try XCTUnwrap(now.at).addingTimeInterval(clock))
    }

    private func parked() throws -> NowSummary {
        let at = try XCTUnwrap(try Fixture.decode(NowSnapshot.self, "now_v1").at)
        let input = NowSummary.Input(socPct: 80, rangeKm: 300, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_disconnected", chargerPowerKw: 0, at: at, packKwh: 135)
        return NowSummary(input: input, now: nil, clock: at.addingTimeInterval(60))
    }

    func testNothingStartsWithTheSwitchOffOrLiveActivitiesNotAllowed() async throws {
        let backend = FakeBackend()
        let m = LiveActivityManager(backend: backend)
        await m.sync(summary: try charging(), switchOn: false, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertTrue(backend.started.isEmpty)
        backend.allowedInSettings = false
        await m.sync(summary: try charging(), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertTrue(backend.started.isEmpty)
    }

    func testAFreshChargeStartsOnceThenUpdates() async throws {
        let backend = FakeBackend()
        let m = LiveActivityManager(backend: backend)
        await m.sync(summary: try charging(), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertEqual(backend.started.count, 1)
        XCTAssertEqual(backend.started[0].target, 80)
        XCTAssertEqual(backend.started[0].state.socPct, 63)
        XCTAssertEqual(backend.started[0].state.powerKw, 11.1)
        XCTAssertEqual(backend.started[0].state.minutesToTarget, 122)
        await m.sync(summary: try charging(clock: 120), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertEqual(backend.started.count, 1, "already running: update, don't start another")
        XCTAssertEqual(backend.updates.count, 1)
    }

    func testStaleOrMissingDataChangesNothing() async throws {
        let backend = FakeBackend()
        let m = LiveActivityManager(backend: backend)
        await m.sync(summary: try charging(), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        await m.sync(summary: try charging(clock: 3 * 3600), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertEqual(backend.endCalls, 0)
        XCTAssertTrue(backend.updates.isEmpty)
        await m.sync(summary: NowSummary(state: nil, now: nil, clock: .now), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertEqual(backend.endCalls, 0)
    }

    func testTheChargeEndingEndsItAndTellsTheServer() async throws {
        let backend = FakeBackend()
        let server = LiveFakeServer()
        let m = LiveActivityManager(backend: backend)
        m.attach(server: server)
        await m.sync(summary: try charging(), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        backend.onToken?("abc123")
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(server.registered, ["abc123"])
        XCTAssertEqual(server.registeredWith.first?.device, "dev")
        XCTAssertEqual(server.registeredWith.first?.vehicle, "veh-1")
        await m.sync(summary: try parked(), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertEqual(backend.endCalls, 1)
        XCTAssertEqual(server.ended, ["abc123"])
    }

    func testWithoutATruckOrDeviceTokenNothingStarts() async throws {
        let backend = FakeBackend()
        let m = LiveActivityManager(backend: backend)
        await m.sync(summary: try charging(), switchOn: true, vehicleID: nil, deviceToken: "dev")
        await m.sync(summary: try charging(), switchOn: true, vehicleID: "veh-1", deviceToken: nil)
        XCTAssertTrue(backend.started.isEmpty)
    }

    func testTurningTheSwitchOffEndsARunningActivity() async throws {
        let backend = FakeBackend()
        let m = LiveActivityManager(backend: backend)
        await m.sync(summary: try charging(), switchOn: true, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertTrue(backend.isRunning)
        await m.sync(summary: try charging(), switchOn: false, vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertFalse(backend.isRunning)
        XCTAssertEqual(backend.endCalls, 1)
    }
}

@MainActor
final class LiveActivityTestButtonTests: XCTestCase {
    private func ready() -> (LiveActivityManager, FakeBackend, LiveFakeServer) {
        let backend = FakeBackend()
        let server = LiveFakeServer()
        let m = LiveActivityManager(backend: backend)
        m.attach(server: server)
        return (m, backend, server)
    }

    func testTheTestStartsALocalActivityRegistersItAndAsksTheServer() async throws {
        let (m, backend, server) = ready()
        let run = Task { await m.sendTest(vehicleID: "veh-1", deviceToken: "dev") }
        try await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertEqual(backend.started.count, 1)
        XCTAssertEqual(backend.started[0].isTest, true)
        XCTAssertEqual(backend.started[0].state, LiveActivityManager.testState)
        XCTAssertEqual(server.liveTests, 0, "not before the activity is registered")
        backend.onToken?("tok1")
        await run.value
        XCTAssertEqual(server.registered, ["tok1"])
        XCTAssertEqual(server.liveTests, 1)
        XCTAssertEqual(m.test, .sent)
        await m.stop()
    }

    func testItSaysWhyWhenTheServerRefuses() async throws {
        let (m, backend, server) = ready()
        server.liveTestResult = APIError.server(status: 404, message: "x")
        let run = Task { await m.sendTest(vehicleID: "veh-1", deviceToken: "dev") }
        try await Task.sleep(nanoseconds: 100_000_000)
        backend.onToken?("tok2")
        await run.value
        XCTAssertEqual(m.test, .failed(LiveActivityManager.testMessage(for: APIError.server(status: 404, message: "x"))))
        XCTAssertFalse(backend.isRunning, "a failed test does not leave its activity on screen")
    }

    func testNotAllowedOrMissingTruckFailsWithoutStartingAnything() async {
        let (m, backend, _) = ready()
        backend.allowedInSettings = false
        await m.sendTest(vehicleID: "veh-1", deviceToken: "dev")
        XCTAssertTrue(backend.started.isEmpty)
        if case .failed = m.test {} else { XCTFail("expected a failure") }
    }

    func testTheMessages() {
        XCTAssertTrue(LiveActivityManager.testMessage(for: APIError.server(status: 409, message: "x")).contains("switch is off"))
        XCTAssertTrue(LiveActivityManager.testMessage(for: APIError.server(status: 429, message: "x")).contains("Wait a minute"))
        XCTAssertTrue(LiveActivityManager.testMessage(for: URLError(.notConnectedToInternet)).contains("Couldn't send"))
    }
}

@MainActor
final class LiveActivitySnapshotTests: XCTestCase {
    private let state = ChargingActivityAttributes.ContentState(socPct: 63, powerKw: 11.1, minutesToTarget: 42)

    func testRenderTheLockScreenView() throws {
        for (name, stale) in [("fresh", false), ("stale", true)] {
            for scheme in [ColorScheme.light, .dark] {
                let view = ChargingLockScreenView(state: state, targetPct: 80, isStale: stale)
                    .frame(width: 360)
                    // The activity's background tint (Theme.background), as on the Lock Screen.
                    .background(Theme.background)
                    .environment(\.colorScheme, scheme)
                try save(view, "liveactivity-\(name)-\(scheme == .dark ? "dark" : "light")")
            }
        }
    }

    /// The Dynamic Island's pieces, laid out roughly as the system does, on its black.
    func testRenderTheDynamicIsland() throws {
        for (name, stale) in [("fresh", false), ("stale", true)] {
            let expanded = VStack(spacing: 8) {
                HStack {
                    ChargingIslandLeading(state: state, targetPct: 80, isStale: stale)
                    Spacer()
                    ChargingIslandTrailing(state: state, isStale: stale)
                }
                ChargingIslandBottom(state: state, targetPct: 80)
            }
            .padding(16)
            .frame(width: 370)
            .background(Color.black, in: RoundedRectangle(cornerRadius: 40))
            .environment(\.colorScheme, .dark)
            try save(expanded, "island-expanded-\(name)")

            let compact = HStack {
                ChargingIslandIcon(isStale: stale)
                Spacer()
                ChargingIslandLevel(state: state)
            }
            .padding(.horizontal, 14)
            .frame(width: 250, height: 37)
            .background(Color.black, in: Capsule())
            .environment(\.colorScheme, .dark)
            try save(compact, "island-compact-\(name)")
        }
    }

    private func save(_ view: some View, _ name: String) throws {
        let renderer = ImageRenderer(content: view)
        renderer.scale = 3
        let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
        XCTAssertGreaterThan(image.size.width, 100)
        let png = try XCTUnwrap(image.pngData())
        let file = "\(name).png"
        let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        attachment.name = file
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
            try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
        }
    }
}
