import XCTest
@testable import Rivolt

private final class FakeServer: PushServer, @unchecked Sendable {
    var apns = true
    var live = false
    var registered: [(token: String, env: String)] = []
    var changed: [(token: String, key: String, on: Bool)] = []
    var unregistered: [String] = []
    var failSwitch = false
    var registerFails = false
    var testResult: Result<Int, Error> = .success(1)
    var tests = 0
    var prefs = PushPreferences()

    func appConfig() async throws -> AppConfig {
        AppConfig(server: "rivolt", apiVersion: 1, appLogin: true, minIosBuild: nil, push: .init(apns: apns, live: live))
    }

    var registerConflict = false
    var installIDs: [String?] = []

    func registerApns(token: String, environment: String, installID: String?) async throws -> PushPreferences {
        if registerConflict { throw APIError.server(status: 409, message: "device_registered_elsewhere") }
        if registerFails { throw APIError.server(status: 500, message: "no") }
        installIDs.append(installID)
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

    func testApns() async throws -> Int {
        tests += 1
        return try testResult.get()
    }

    var activityTokens: [String] = []
    var endedActivityTokens: [String] = []

    func registerActivity(deviceToken: String, activityToken: String, vehicleID: String) async throws {
        activityTokens.append(activityToken)
    }

    func endActivity(activityToken: String) async throws {
        endedActivityTokens.append(activityToken)
    }

    func testLiveActivity() async throws {}
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
        return NotificationManager(system: system, defaults: defaults, environment: "sandbox", installID: { "test-install-id" })
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

    func testTheInstallIDGoesWithTheToken() async {
        let system = FakeSystem()
        let server = FakeServer()
        let m = make(system)
        await m.signedIn(server: server)
        await m.turnOn()
        await m.register(deviceToken: Data([0x01]))
        XCTAssertEqual(server.installIDs, ["test-install-id"])
    }

    func testAnotherAccountsTokenIsRefusedWithNoErrorOnScreen() async {
        let system = FakeSystem()
        let server = FakeServer()
        server.registerConflict = true
        let m = make(system)
        await m.signedIn(server: server)
        await m.turnOn()
        await m.register(deviceToken: Data([0x01]))
        XCTAssertEqual(m.registration, .idle, "silent: not failed, not registered")
        XCTAssertFalse(m.isOn)
        // The next launch tries again, and gets in once the token is this account's.
        server.registerConflict = false
        await m.register(deviceToken: Data([0x01]))
        XCTAssertTrue(m.isOn)
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

    func testTheTestNotificationIsSentOnlyWhileOnAndSaysHowManyOrWhy() async {
        let system = FakeSystem()
        let server = FakeServer()
        let m = make(system)
        await m.signedIn(server: server)
        await m.sendTest()
        XCTAssertEqual(server.tests, 0, "nothing is sent while notifications are off")
        XCTAssertEqual(m.test, .idle)
        await m.turnOn()
        await m.register(deviceToken: Data([0x03]))
        await m.sendTest()
        XCTAssertEqual(m.test, .sent(1))
        server.testResult = .failure(APIError.server(status: 404, message: "x"))
        await m.sendTest()
        XCTAssertEqual(m.test, .failed(NotificationManager.testMessage(for: APIError.server(status: 404, message: "x"))))
        XCTAssertTrue(NotificationManager.testMessage(for: APIError.server(status: 429, message: "x")).contains("Wait a minute"))
        XCTAssertTrue(NotificationManager.testMessage(for: URLError(.notConnectedToInternet)).contains("Couldn't send"))
        await m.turnOff()
        XCTAssertEqual(m.test, .idle)
    }

    func testARepeatedTokenCallbackKeepsAnAlreadyRegisteredPhoneRegistered() async {
        let system = FakeSystem()
        let server = FakeServer()
        let m = make(system)
        await m.signedIn(server: server)
        await m.turnOn()
        await m.register(deviceToken: Data([0x05]))
        XCTAssertTrue(m.isOn)
        // iOS repeats the callback and the server then fails: the phone stays on, it is not flipped to "failed".
        server.registerFails = true
        await m.register(deviceToken: Data([0x05]))
        XCTAssertTrue(m.isOn)
        // A later sign-in pass does not mark it as registering again either.
        await m.signedIn(server: server)
        XCTAssertEqual(m.registration, .registered)
    }

    func testAFirstRegistrationThatFailsSaysSo() async {
        let system = FakeSystem()
        let server = FakeServer()
        server.registerFails = true
        let m = make(system)
        await m.signedIn(server: server)
        await m.turnOn()
        await m.register(deviceToken: Data([0x06]))
        XCTAssertFalse(m.isOn)
        if case .failed = m.registration {} else { XCTFail("expected a failed state, got \(m.registration)") }
    }

    func testTheLiveActivitySwitchIsOfferedOnlyWhenTheServerSendsLiveUpdates() async {
        let system = FakeSystem()
        let server = FakeServer()
        let m = make(system)
        await m.signedIn(server: server)
        XCTAssertFalse(m.liveOffered)
        server.live = true
        await m.signedIn(server: server)
        XCTAssertTrue(m.liveOffered)
        await m.signedOut()
        XCTAssertFalse(m.liveOffered)
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

    func testLeftOpenIsOptInAndHiddenUntilTheServerSendsIt() throws {
        // A server that predates it: off, and the row is hidden.
        let old = try decode(#"{"on_charging_done":true}"#)
        XCTAssertFalse(old.onLeftOpen)
        XCTAssertFalse(old.leftOpenKnown)
        // A server that has it: shown, off until turned on.
        let off = try decode(#"{"on_left_open":false}"#)
        XCTAssertFalse(off.onLeftOpen)
        XCTAssertTrue(off.leftOpenKnown)
        let on = try decode(#"{"on_left_open":true}"#)
        XCTAssertTrue(on.onLeftOpen)
        // The help says what it covers and what it does not, and never promises to always tell you.
        let help = try XCTUnwrap(PushKind.leftOpen.help)
        XCTAssertTrue(help.contains("Not windows") && help.contains("can be missed if the truck is offline") && help.contains("Off until you turn it on"), help)
    }

    func testTireLowIsOptInAndHiddenUntilTheServerSendsIt() throws {
        let old = try decode(#"{"on_charging_done":true}"#)
        XCTAssertFalse(old.onTireLow)
        XCTAssertFalse(old.tireLowKnown)
        let off = try decode(#"{"on_tire_low":false}"#)
        XCTAssertFalse(off.onTireLow)
        XCTAssertTrue(off.tireLowKnown)
        XCTAssertTrue(try decode(#"{"on_tire_low":true}"#).onTireLow)
        // The help states the boundary: it repeats the truck, is no replacement for the truck's own warning, can be
        // missed, and is off until turned on. It promises nothing and gives no number or advice.
        let help = try XCTUnwrap(PushKind.tireLow.help)
        XCTAssertTrue(help.contains("repeats what the truck reports") && help.contains("doesn't replace the truck's own warning")
                      && help.contains("can be missed if the truck is offline or asleep") && help.contains("Off until you turn it on"), help)
        XCTAssertEqual(PushKind.tireLow.title, "Low tire pressure")
        XCTAssertEqual(PushKind.tireLow.wireKey, "on_tire_low")
    }

    func testEveryKindHasItsWebWordingAndWireKey() {
        XCTAssertEqual(PushKind.allCases.count, 8)
        XCTAssertEqual(PushKind.chargingDone.title, "Charging session completes")
        XCTAssertEqual(PushKind.plugInReminder.wireKey, "on_plug_in_reminder")
        XCTAssertEqual(PushKind.leftOpen.wireKey, "on_left_open")
        var p = PushPreferences()
        for k in PushKind.allCases {
            p[k] = !p[k]
        }
        XCTAssertEqual(p.onChargingDone, false)
        XCTAssertEqual(p.onMilestones, true)
        XCTAssertEqual(p.onLeftOpen, true)
        XCTAssertEqual(p.onTireLow, true)
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

/// The extension's failure path: whatever goes wrong, nothing is returned and the generic alert stays.
private final class FetchStub: URLProtocol {
    nonisolated(unsafe) static var reply: (Int, Data)?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let (code, data) = Self.reply else {
            client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
            return
        }
        let resp = HTTPURLResponse(url: request.url!, statusCode: code, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private struct FetchTokens: TokenStore {
    let token: String?
    func token(for server: URL) throws -> String? { token }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

final class NotificationFailurePathTests: XCTestCase {
    private func session() -> URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [FetchStub.self]
        return URLSession(configuration: c)
    }

    func testEveryFailureReturnsNilSoTheGenericAlertStays() async {
        let id = "3f2b8c1e-aaaa-4bbb-8ccc-0123456789ab"
        FetchStub.reply = (200, Data(#"{"title":"Charging complete","body":"At 80%."}"#.utf8))
        let ok = await NotificationFetch.text(id: id, tokens: FetchTokens(token: "tok"), session: session())
        XCTAssertEqual(ok, NotificationFetch.Text(title: "Charging complete", body: "At 80%."))

        let noToken = await NotificationFetch.text(id: id, tokens: FetchTokens(token: nil), session: session())
        XCTAssertNil(noToken, "signed out: no fetch, generic alert")
        FetchStub.reply = (404, Data())
        let gone = await NotificationFetch.text(id: id, tokens: FetchTokens(token: "tok"), session: session())
        XCTAssertNil(gone)
        FetchStub.reply = (200, Data("not json".utf8))
        let junk = await NotificationFetch.text(id: id, tokens: FetchTokens(token: "tok"), session: session())
        XCTAssertNil(junk)
        FetchStub.reply = nil
        let offline = await NotificationFetch.text(id: id, tokens: FetchTokens(token: "tok"), session: session())
        XCTAssertNil(offline)
    }

    func testANotificationShownInTheForegroundHasABannerAListAndASound() {
        let o = NotificationPresentation.foreground
        XCTAssertTrue(o.contains(.banner))
        XCTAssertTrue(o.contains(.list))
        XCTAssertTrue(o.contains(.sound))
    }
}
