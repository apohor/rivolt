import XCTest
@testable import Rivolt

private final class FakeServer: PushServer, @unchecked Sendable {
    var apns = true
    var registered: [(token: String, env: String)] = []
    var changed: [(token: String, key: String, on: Bool)] = []
    var unregistered: [String] = []
    var failSwitch = false
    var prefs = PushPreferences()

    func appConfig() async throws -> AppConfig {
        AppConfig(server: "rivolt", apiVersion: 1, appLogin: true, minIosBuild: nil, push: .init(apns: apns))
    }

    func registerApns(token: String, environment: String) async throws -> PushPreferences {
        registered.append((token, environment))
        return prefs
    }

    func setApnsPreference(token: String, wireKey: String, on: Bool) async throws {
        if failSwitch { throw APIError.server(status: 500, message: "no") }
        changed.append((token, wireKey, on))
    }

    func unregisterApns(token: String) async throws {
        unregistered.append(token)
    }
}

@MainActor
private final class FakeSystem: NotificationSystem {
    var status: PushAuthorization = .notDetermined
    var grant = true
    var prompts = 0
    var registerCalls = 0
    var unregisterCalls = 0

    func authorizationStatus() async -> PushAuthorization { status }

    func requestAuthorization() async -> Bool {
        prompts += 1
        status = grant ? .authorized : .denied
        return grant
    }

    func registerForRemoteNotifications() { registerCalls += 1 }
    func unregisterForRemoteNotifications() { unregisterCalls += 1 }
}

@MainActor
final class NotificationManagerTests: XCTestCase {
    private func make(_ system: FakeSystem) -> NotificationManager {
        let defaults = UserDefaults(suiteName: "rivolt.tests.push.\(UUID().uuidString)")!
        return NotificationManager(system: system, defaults: defaults, environment: "sandbox")
    }

    func testNothingIsOfferedOrAskedWhenTheServerHasNoApns() async {
        let system = FakeSystem()
        let server = FakeServer()
        server.apns = false
        let m = make(system)
        await m.signedIn(server: server)
        XCTAssertFalse(m.offered)
        XCTAssertFalse(m.shouldAskInApp)
        await m.turnOn()
        XCTAssertEqual(system.prompts, 0)
        XCTAssertEqual(system.registerCalls, 0)
    }

    func testTheCardShowsButIosIsNotAskedUntilThePersonTapsTurnOn() async {
        let system = FakeSystem()
        let m = make(system)
        await m.signedIn(server: FakeServer())
        XCTAssertTrue(m.offered)
        XCTAssertTrue(m.shouldAskInApp)
        XCTAssertEqual(system.prompts, 0, "signing in alone must not raise the iOS prompt")
        XCTAssertEqual(system.registerCalls, 0)
    }

    func testTurnOnPromptsThenRegistersTheTokenWithTheServer() async {
        let system = FakeSystem()
        let server = FakeServer()
        let m = make(system)
        await m.signedIn(server: server)
        await m.turnOn()
        XCTAssertEqual(system.prompts, 1)
        XCTAssertEqual(system.registerCalls, 1)
        XCTAssertEqual(m.authorization, .authorized)
        await m.register(deviceToken: Data([0xab, 0x01, 0xff]))
        XCTAssertEqual(m.deviceToken, "ab01ff")
        XCTAssertEqual(server.registered.map { $0.token }, ["ab01ff"])
        XCTAssertEqual(server.registered.map { $0.env }, ["sandbox"])
        XCTAssertTrue(m.isOn)
        XCTAssertEqual(m.preferences, PushPreferences())
        XCTAssertFalse(m.shouldAskInApp)
    }

    func testDeniedNeverRegisters() async {
        let system = FakeSystem()
        system.grant = false
        let m = make(system)
        await m.signedIn(server: FakeServer())
        await m.turnOn()
        XCTAssertEqual(m.authorization, .denied)
        XCTAssertEqual(system.registerCalls, 0)
        XCTAssertFalse(m.isOn)
        XCTAssertFalse(m.shouldAskInApp)
    }

    func testAlreadyAllowedRegistersAgainOnLaunchWithoutAPrompt() async {
        let system = FakeSystem()
        system.status = .authorized
        let m = make(system)
        await m.signedIn(server: FakeServer())
        XCTAssertEqual(system.prompts, 0)
        XCTAssertEqual(system.registerCalls, 1)
    }

    func testNotNowIsRemembered() {
        let system = FakeSystem()
        let defaults = UserDefaults(suiteName: "rivolt.tests.push.dismiss.\(UUID().uuidString)")!
        let m = NotificationManager(system: system, defaults: defaults, environment: "sandbox")
        m.dismissPrompt()
        XCTAssertTrue(m.promptDismissed)
        XCTAssertTrue(NotificationManager(system: system, defaults: defaults, environment: "sandbox").promptDismissed)
    }

    func testSignOutForgetsThePhoneOnTheServerAndLocally() async {
        let system = FakeSystem()
        let server = FakeServer()
        let m = make(system)
        await m.signedIn(server: server)
        await m.turnOn()
        await m.register(deviceToken: Data([0x01]))
        await m.signedOut()
        XCTAssertEqual(server.unregistered, ["01"])
        XCTAssertEqual(system.unregisterCalls, 1)
        XCTAssertNil(m.deviceToken)
        XCTAssertNil(m.preferences)
        XCTAssertFalse(m.offered)
    }

    func testASwitchIsShownAtOnceAndPutBackWhenTheServerRefuses() async {
        let system = FakeSystem()
        let server = FakeServer()
        let m = make(system)
        await m.signedIn(server: server)
        await m.turnOn()
        await m.register(deviceToken: Data([0x02]))
        await m.set(.milestones, to: true)
        XCTAssertEqual(m.preferences?.onMilestones, true)
        XCTAssertEqual(server.changed.map { $0.key }, ["on_milestones"])
        server.failSwitch = true
        await m.set(.anomaly, to: false)
        XCTAssertEqual(m.preferences?.onAnomaly, true, "a refused switch goes back")
        XCTAssertNotNil(m.switchError)
    }

    func testTheTokenIsHexAndTheEnvironmentIsKnown() {
        XCTAssertEqual(NotificationManager.hex(Data([0x00, 0x0f, 0xa0])), "000fa0")
        XCTAssertTrue(["sandbox", "production"].contains(NotificationManager.defaultEnvironment))
    }
}

final class PushPreferencesTests: XCTestCase {
    private func decode(_ json: String) throws -> PushPreferences {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return try d.decode(PushPreferences.self, from: Data(json.utf8))
    }

    func testDecodesTheServerShape() throws {
        let p = try decode(#"{"on_charging_done":false,"on_plug_in_reminder":true,"on_anomaly":true,"on_software_update":false,"on_milestones":true}"#)
        XCTAssertEqual(p.onChargingDone, false)
        XCTAssertEqual(p.onPlugInReminder, true)
        XCTAssertEqual(p.onSoftwareUpdate, false)
        XCTAssertEqual(p.onMilestones, true)
    }

    func testAMissingKindKeepsItsDefault() throws {
        let p = try decode(#"{"on_charging_done":false}"#)
        XCTAssertEqual(p.onChargingDone, false)
        XCTAssertTrue(p.onAnomaly)
        XCTAssertTrue(p.onSoftwareUpdate)
        XCTAssertFalse(p.onPlugInReminder, "the plug-in reminder is opt-in")
        XCTAssertFalse(p.onMilestones, "milestones are opt-in")
    }

    func testEveryKindHasItsWebWordingAndWireKey() {
        XCTAssertEqual(PushKind.allCases.count, 5)
        XCTAssertEqual(PushKind.chargingDone.title, "Charging session completes")
        XCTAssertEqual(PushKind.plugInReminder.wireKey, "on_plug_in_reminder")
        var p = PushPreferences()
        for k in PushKind.allCases {
            p[k] = !p[k]
        }
        XCTAssertEqual(p.onChargingDone, false)
        XCTAssertEqual(p.onMilestones, true)
    }
}

final class NotificationFetchTests: XCTestCase {
    private let server = URL(string: "https://rivolt.dev")!

    func testOnlyARandomLookingIdIsUsed() {
        XCTAssertEqual(NotificationFetch.id(from: ["id": "3f2b8c1e-aaaa-4bbb-8ccc-0123456789ab", "kind": "charging_done"]), "3f2b8c1e-aaaa-4bbb-8ccc-0123456789ab")
        XCTAssertNil(NotificationFetch.id(from: ["kind": "charging_done"]))
        XCTAssertNil(NotificationFetch.id(from: ["id": "../../api/auth/me"]))
        XCTAssertNil(NotificationFetch.id(from: ["id": "abc?x=1"]))
        XCTAssertNil(NotificationFetch.id(from: ["id": ""]))
        XCTAssertNil(NotificationFetch.id(from: ["id": 42]))
    }

    func testTheRequestGoesToTheNotificationWithTheBearerToken() throws {
        let req = try XCTUnwrap(NotificationFetch.request(id: "3f2b-01", server: server, token: "tok"))
        XCTAssertEqual(req.url?.absoluteString, "https://rivolt.dev/api/push/notifications/3f2b-01")
        XCTAssertEqual(req.httpMethod, "GET")
        XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer tok")
        XCTAssertNil(NotificationFetch.request(id: "../x", server: server, token: "tok"))
    }

    func testDecodesTitleAndBodyAndIgnoresAnEmptyAnswer() {
        let ok = NotificationFetch.decode(Data(#"{"title":"Charging complete","body":"Your truck is at 80%."}"#.utf8))
        XCTAssertEqual(ok, NotificationFetch.Text(title: "Charging complete", body: "Your truck is at 80%."))
        XCTAssertNil(NotificationFetch.decode(Data(#"{"title":"","body":" "}"#.utf8)))
        XCTAssertNil(NotificationFetch.decode(Data("not json".utf8)))
    }
}
