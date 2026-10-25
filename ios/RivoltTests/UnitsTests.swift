import XCTest
@testable import Rivolt

private final class UnitsStub: URLProtocol {
    nonisolated(unsafe) static var body = ""
    nonisolated(unsafe) static var seen: [(method: String, path: String, body: String)] = []
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        var sent = ""
        if let d = request.httpBody ?? request.httpBodyStream.flatMap({ s -> Data in
            s.open(); defer { s.close() }
            var out = Data(); var buf = [UInt8](repeating: 0, count: 1024)
            while s.hasBytesAvailable { let n = s.read(&buf, maxLength: buf.count); if n <= 0 { break }; out.append(buf, count: n) }
            return out }) { sent = String(data: d, encoding: .utf8) ?? "" }
        Self.seen.append((request.httpMethod ?? "", request.url?.path ?? "", sent))
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(Self.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class UnitsToken: TokenStore, @unchecked Sendable {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

@MainActor
final class UnitsTests: XCTestCase {
    override func setUp() { DisplayUnits.reset() }
    override func tearDown() { DisplayUnits.reset() }

    private func state() async throws -> AppState {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [UnitsStub.self]
        UnitsStub.seen = []
        UnitsStub.body = #"{"username":"sam","units":{"distance_unit":"km","temperature_unit":"c","distance_source":"user","temperature_source":"follows_distance"}}"#
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "units-tests-\(UUID().uuidString)"))
        let s = AppState(tokens: UnitsToken(), defaults: defaults, session: URLSession(configuration: c))
        await s.bootstrap()
        return s
    }

    func testTheRegionRule() {
        for r in ["US", "GB", "LR", "MM"] { XCTAssertTrue(DisplayUnits.milesRegion(r), r) }
        for r in ["DE", "CA", "AU", "JP"] { XCTAssertFalse(DisplayUnits.milesRegion(r), r) }
    }

    func testMeCarriesTheUnitsAndSignInAppliesThem() async throws {
        let me = try JSON.decoder().decode(AuthUser.self, from: Data(#"{"username":"sam","units":{"distance_unit":"km","temperature_unit":"c","distance_source":"user","temperature_source":"user"}}"#.utf8))
        XCTAssertEqual(me.units?.distanceUnit, "km")
        XCTAssertEqual(me.units?.temperatureForDisplay, "c")
        let s = try await state()
        XCTAssertEqual(s.phase, .loggedIn(username: "sam"))
        XCTAssertTrue(DisplayUnits.distanceMetric)
        XCTAssertTrue(DisplayUnits.celsius, "temperature follows the distance")
        XCTAssertTrue(DisplayUnits.temperatureFollowsDistance)
        XCTAssertEqual(s.unitsRevision, 1, "the screens read the new units")
    }

    func testAPickGoesToTheAccountAndTheScreensRebuildWhenTheUnitsScreenCloses() async throws {
        let s = try await state()
        UnitsStub.seen = []
        UnitsStub.body = #"{"distance_unit":"mi","temperature_unit":"c","distance_source":"user","temperature_source":"user"}"#
        let ok = await s.setUnits(distance: "mi", temperature: "c")
        XCTAssertTrue(ok)
        let patch = try XCTUnwrap(UnitsStub.seen.first)
        XCTAssertEqual(patch.method, "PATCH")
        XCTAssertEqual(patch.path, "/api/settings/units")
        XCTAssertTrue(patch.body.contains("\"distance_unit\":\"mi\"") && patch.body.contains("\"temperature_unit\":\"c\"") && !patch.body.contains("seed"), patch.body)
        XCTAssertFalse(DisplayUnits.distanceMetric)
        XCTAssertTrue(DisplayUnits.celsius, "the pick: miles with °C")
        XCTAssertFalse(DisplayUnits.temperatureFollowsDistance)
        let before = s.unitsRevision
        s.unitsScreenClosed()
        XCTAssertEqual(s.unitsRevision, before + 1)
        s.unitsScreenClosed()
        XCTAssertEqual(s.unitsRevision, before + 1, "once")
    }

    func testAnAccountWithNoUnitsIsSeededOnceFromTheRegion() async throws {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [UnitsStub.self]
        UnitsStub.seen = []
        UnitsStub.body = #"{"username":"sam","units":{"distance_unit":"mi","temperature_unit":"f","distance_source":"unset","temperature_source":"follows_distance"}}"#
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "units-tests-\(UUID().uuidString)"))
        let s = AppState(tokens: UnitsToken(), defaults: defaults, session: URLSession(configuration: c))
        await s.bootstrap()
        let seed = try XCTUnwrap(UnitsStub.seen.first { $0.method == "PATCH" })
        XCTAssertTrue(seed.body.contains("\"seed\":true"), seed.body)
        XCTAssertTrue(seed.body.contains("\"distance_unit\":\"\(DisplayUnits.regionIsMetric ? "km" : "mi")\""), seed.body)
        XCTAssertFalse(seed.body.contains("temperature_unit"), "the temperature follows the distance")
    }

    func testTemperatureReadsInTheAccountsUnit() throws {
        DisplayUnits.apply(distance: "km", temperature: "f")
        XCTAssertEqual(NowComfort.wholeDegrees(celsius: 20), "68°F")
        DisplayUnits.apply(distance: "mi", temperature: "c")
        XCTAssertEqual(NowComfort.wholeDegrees(celsius: 20), "20°C")
        DisplayUnits.apply(distance: "mi", temperature: nil)
        XCTAssertEqual(NowComfort.wholeDegrees(celsius: 20), "68°F", "following the distance: miles, °F")
        XCTAssertFalse(NowFormat.usesMetric)
    }
}
