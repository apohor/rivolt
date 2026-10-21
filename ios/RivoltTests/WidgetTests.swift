import XCTest
@testable import Rivolt

private struct NoTokens: TokenStore {
    func token(for server: URL) throws -> String? { nil }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

private struct FixedToken: TokenStore {
    let value: String
    func token(for server: URL) throws -> String? { value }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

/// Answers by path, so the widget's fetch can be tested without a network.
private final class StubProtocol: URLProtocol {
    nonisolated(unsafe) static var handler: ((URLRequest) -> (Int, Data)?)?
    nonisolated(unsafe) static var seen: [URLRequest] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.seen.append(request)
        guard let (code, data) = Self.handler?(request) else {
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

final class WidgetModelTests: XCTestCase {
    private func fixtures() throws -> (NowSummary.Input, NowSnapshot) {
        (NowSummary.Input(try Fixture.decode(VehicleState.self, "state")), try Fixture.decode(NowSnapshot.self, "now_v1"))
    }

    func testASnapshotHoldsOnlyTheShownFields() throws {
        let (input, now) = try fixtures()
        let s = try XCTUnwrap(WidgetSnapshot.make(input: input, now: now, clock: try XCTUnwrap(now.at).addingTimeInterval(60)))
        XCTAssertEqual(s.stateLabel, "Charging")
        XCTAssertTrue(s.isCharging)
        XCTAssertEqual(s.chargeTargetPct, 80)
        let json = String(decoding: try JSONEncoder().encode(s), as: UTF8.self).lowercased()
        for forbidden in ["vin", "lat", "lon", "name", "location"] {
            XCTAssertFalse(json.contains(forbidden), "the stored snapshot must not carry \(forbidden): \(json)")
        }
    }

    func testNoChargeLevelOrRangeMeansNothingToShow() {
        XCTAssertNil(WidgetSnapshot.make(input: nil, now: nil, clock: .now))
    }

    func testFreshThenStaleAtTheStatesCadence() throws {
        let (input, now) = try fixtures()
        let at = try XCTUnwrap(now.at)
        let s = try XCTUnwrap(WidgetSnapshot.make(input: input, now: now, clock: at))
        XCTAssertEqual(s.freshness(at: at.addingTimeInterval(60)), .fresh)
        XCTAssertEqual(s.freshness(at: at.addingTimeInterval(11 * 60)), .stale)
        let stale = s.status(at: at.addingTimeInterval(3 * 3600))
        XCTAssertTrue(stale.text.hasPrefix("Updated"), stale.text)
        XCTAssertEqual(stale.tone, .stale)
        XCTAssertEqual(stale.icon, "clock.badge.exclamationmark")
        XCTAssertEqual(s.status(at: at.addingTimeInterval(60)).text, "Charging · 11.1 kW")
    }

    func testTheTimelineFlipsToStaleOnTime() throws {
        let (input, now) = try fixtures()
        let at = try XCTUnwrap(now.at)
        let s = try XCTUnwrap(WidgetSnapshot.make(input: input, now: now, clock: at))
        let entries = WidgetTimelineBuilder.entries(.data(s), at: at.addingTimeInterval(60))
        XCTAssertEqual(entries.count, 2)
        XCTAssertGreaterThan(entries[1].date, at.addingTimeInterval(10 * 60))
        XCTAssertTrue(s.isStale(at: entries[1].date))
        // Already stale: one entry.
        XCTAssertEqual(WidgetTimelineBuilder.entries(.data(s), at: at.addingTimeInterval(3 * 3600)).count, 1)
        XCTAssertEqual(WidgetTimelineBuilder.entries(.signedOut, at: at).count, 1)
    }

    func testRefreshAsksForLessOftenWhenNothingIsHappening() throws {
        let (input, now) = try fixtures()
        let at = try XCTUnwrap(now.at)
        let charging = try XCTUnwrap(WidgetSnapshot.make(input: input, now: now, clock: at))
        XCTAssertEqual(WidgetTimelineBuilder.nextRefresh(.data(charging), at: at.addingTimeInterval(60)).timeIntervalSince(at.addingTimeInterval(60)), 15 * 60)
        XCTAssertEqual(WidgetTimelineBuilder.nextRefresh(.data(charging), at: at.addingTimeInterval(3 * 3600)).timeIntervalSince(at.addingTimeInterval(3 * 3600)), 60 * 60)
        var parked = charging
        parked.isCharging = false
        parked.stateLabel = "Parked"
        XCTAssertEqual(WidgetTimelineBuilder.nextRefresh(.data(parked), at: at.addingTimeInterval(60)).timeIntervalSince(at.addingTimeInterval(60)), 30 * 60)
        XCTAssertEqual(WidgetTimelineBuilder.nextRefresh(.signedOut, at: at).timeIntervalSince(at), 60 * 60)
    }
}

final class WidgetStoreTests: XCTestCase {
    private func store() -> WidgetStore {
        WidgetStore(defaults: UserDefaults(suiteName: "rivolt.tests.widget.\(UUID().uuidString)")!)
    }

    private let server = URL(string: "https://rivolt.dev")!

    func testRoundTripAndClear() {
        let s = store()
        s.setSession(vehicleID: "veh-123", server: server)
        s.save(.sample)
        XCTAssertEqual(s.vehicleID, "veh-123")
        XCTAssertEqual(s.server, server)
        XCTAssertEqual(s.snapshot, .sample)
        s.clear()
        XCTAssertNil(s.vehicleID)
        XCTAssertNil(s.server)
        XCTAssertNil(s.snapshot)
    }

    func testAnotherServerOrTruckDropsTheOldNumbers() {
        let s = store()
        s.setSession(vehicleID: "veh-1", server: server)
        s.save(.sample)
        s.setSession(vehicleID: "veh-1", server: server)
        XCTAssertNotNil(s.snapshot, "the same truck keeps its snapshot")
        s.setSession(vehicleID: "veh-2", server: server)
        XCTAssertNil(s.snapshot)
        s.save(.sample)
        s.setSession(vehicleID: "veh-2", server: URL(string: "https://rivolt.example.com")!)
        XCTAssertNil(s.snapshot)
    }

    func testABadVehicleIdIsNeverStored() {
        let s = store()
        s.setSession(vehicleID: "../../etc", server: server)
        XCTAssertNil(s.vehicleID)
    }

    func testResolverKeepsTheLastKnownOnAFailureAndClearsOnRefusal() {
        let s = store()
        s.setSession(vehicleID: "veh-1", server: server)
        XCTAssertEqual(WidgetResolver.resolve(.failed, store: s), .unavailable)
        XCTAssertEqual(WidgetResolver.resolve(.snapshot(.sample), store: s), .data(.sample))
        XCTAssertEqual(WidgetResolver.resolve(.failed, store: s), .data(.sample), "a failed refresh shows the last values")
        XCTAssertEqual(WidgetResolver.resolve(.revoked, store: s), .signedOut)
        XCTAssertNil(s.snapshot)
        XCTAssertNil(s.vehicleID)
        s.save(.sample)
        XCTAssertEqual(WidgetResolver.resolve(.signedOut, store: s), .signedOut)
        XCTAssertNil(s.snapshot)
    }
}

final class WidgetFetchTests: XCTestCase {
    private let server = URL(string: "https://rivolt.dev")!

    private func session() -> URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [StubProtocol.self]
        return URLSession(configuration: c)
    }

    private func store() -> WidgetStore {
        let s = WidgetStore(defaults: UserDefaults(suiteName: "rivolt.tests.widgetfetch.\(UUID().uuidString)")!)
        s.setSession(vehicleID: "veh-1", server: server)
        return s
    }

    override func setUp() {
        StubProtocol.handler = nil
        StubProtocol.seen = []
    }

    private func answer(state: Int = 200, now: Int = 200) throws {
        let stateData = try Fixture.data("state")
        let nowData = try Fixture.data("now_v1")
        StubProtocol.handler = { req in
            let path = req.url?.path ?? ""
            if path.hasSuffix("/now") { return (now, nowData) }
            if path.hasPrefix("/api/state/") { return (state, stateData) }
            return nil
        }
    }

    func testIdsAndRequestsStayOnTheTokensOrigin() {
        XCTAssertTrue(WidgetFetch.validID("a1b2-c3_d4"))
        for bad in ["", "../x", "a/b", "a?b", "a b", "é", String(repeating: "a", count: 65)] { XCTAssertFalse(WidgetFetch.validID(bad), bad) }
        let ok = WidgetFetch.request("/api/state/veh-1", server: server, token: "tok")
        XCTAssertEqual(ok?.url?.absoluteString, "https://rivolt.dev/api/state/veh-1")
        XCTAssertEqual(ok?.value(forHTTPHeaderField: "Authorization"), "Bearer tok")
        XCTAssertEqual(ok?.httpMethod, "GET")
        for bad in ["/other", "//evil.example/api/x", "/api/x?y=1", "/api/x#y", "api/x", "https://evil.example/api/x"] {
            XCTAssertNil(WidgetFetch.request(bad, server: server, token: "tok"), bad)
        }
    }

    func testNoTokenIsSignedOutAndAsksNothing() async {
        let outcome = await WidgetFetch.load(store: store(), tokens: NoTokens(), session: session())
        XCTAssertEqual(outcome, .signedOut)
        XCTAssertTrue(StubProtocol.seen.isEmpty)
    }

    func testReadsTheTwoEndpointsOnTheServerOnlyAndBuildsASnapshot() async throws {
        try answer()
        let outcome = await WidgetFetch.load(store: store(), tokens: FixedToken(value: "tok"), session: session(), clock: .now)
        guard case .snapshot(let s) = outcome else { return XCTFail("expected a snapshot, got \(outcome)") }
        XCTAssertEqual(s.stateLabel, "Charging")
        XCTAssertEqual(Set(StubProtocol.seen.compactMap { $0.url?.host }), ["rivolt.dev"])
        XCTAssertEqual(Set(StubProtocol.seen.compactMap { $0.url?.path }), ["/api/state/veh-1", "/api/vehicles/veh-1/now"])
        XCTAssertTrue(StubProtocol.seen.allSatisfy { $0.httpMethod == "GET" && $0.value(forHTTPHeaderField: "Authorization") == "Bearer tok" })
        let stored = String(decoding: try JSONEncoder().encode(s), as: UTF8.self).lowercased()
        XCTAssertFalse(stored.contains("lat"), "a location in the answer is never kept")
    }

    func testAnUnauthorizedForbiddenOrGoneTruckIsRevoked() async throws {
        for code in [401, 403, 404] {
            try answer(state: code)
            let outcome = await WidgetFetch.load(store: store(), tokens: FixedToken(value: "tok"), session: session())
            XCTAssertEqual(outcome, .revoked, "state \(code)")
        }
        for code in [401, 403] {
            try answer(now: code)
            let outcome = await WidgetFetch.load(store: store(), tokens: FixedToken(value: "tok"), session: session())
            XCTAssertEqual(outcome, .revoked, "now \(code)")
        }
    }

    func testAServerWithoutNowStillShowsTheStateAlone() async throws {
        try answer(now: 404)
        let outcome = await WidgetFetch.load(store: store(), tokens: FixedToken(value: "tok"), session: session())
        guard case .snapshot = outcome else { return XCTFail("expected a snapshot, got \(outcome)") }
    }

    func testAServerErrorOrNoNetworkIsAFailureNotAnEmptyWidget() async throws {
        try answer(state: 500)
        let serverError = await WidgetFetch.load(store: store(), tokens: FixedToken(value: "tok"), session: session())
        XCTAssertEqual(serverError, .failed)
        StubProtocol.handler = nil
        let offline = await WidgetFetch.load(store: store(), tokens: FixedToken(value: "tok"), session: session())
        XCTAssertEqual(offline, .failed)
    }
}
