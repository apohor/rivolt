import XCTest
@testable import Rivolt

/// Activity → Efficiency: the server's response (a synthetic fixture marshalled from insights.Efficiency, strictly
/// decoded by the server's own fixture test too), the web's words and numbers, the model, and the request.
@MainActor
final class EfficiencyTests: XCTestCase {
    private func fixture() throws -> EfficiencyInsights { try Fixture.decode(EfficiencyInsights.self, "efficiency") }

    func testTheResponseDecodes() throws {
        let e = try fixture()
        XCTAssertEqual(e.units, "imperial")
        XCTAssertEqual(e.efficiencyUnit, "mi/kWh")
        XCTAssertEqual(e.total.drives, 7)
        XCTAssertEqual(e.total.efficiency, 2.71)
        XCTAssertEqual(e.byTemperature.map(\.efficiency), [nil, 2.4, 2.79, 2.83, 2.14])
        XCTAssertEqual(e.byTemperature.last?.notEnoughData, true)
        XCTAssertEqual(e.byDriveMode.map(\.label), ["All-Purpose", "Sport", "Conserve"])
        XCTAssertEqual(e.excluded?.noEnergy, 1)
    }

    func testTheWebsWordsAndNumbers() throws {
        let e = try fixture()
        XCTAssertTrue(EfficiencyLogic.ready(e))
        XCTAssertEqual(EfficiencyLogic.groups(e).map(\.title), ["By outside temperature", "By speed", "By drive mode"])
        XCTAssertEqual(EfficiencyLogic.summary(e), "7 drives · 238 mi · 88 kWh · 1 left out (no energy recorded)")
        XCTAssertEqual(EfficiencyLogic.scaleMax(e.byTemperature, overall: 2.71), 2.83 * 1.15, accuracy: 1e-9)
        XCTAssertEqual(EfficiencyLogic.scaleMax([], overall: nil), 1.15, accuracy: 1e-9)
        // The best on enough distance: 86 °F and above is thin, so 68–86 °F.
        XCTAssertEqual(EfficiencyLogic.best(e.byTemperature), e.byTemperature[3].key)
        XCTAssertEqual(EfficiencyLogic.split("Highway (55–70 mph)").main, "Highway")
        XCTAssertEqual(EfficiencyLogic.split("Highway (55–70 mph)").sub, "55–70 mph")
        XCTAssertEqual(EfficiencyLogic.split("32–50 °F").main, "32–50 °F")
        XCTAssertEqual(EfficiencyLogic.notEnough(e, month: "October 2026"),
                       "Not enough data yet for October 2026: efficiency needs drives with recorded energy, at least 25 mi.")
    }

    /// "Below 32 °F" never leaves "°F" alone on a line: a no-break space joins each number to its unit.
    func testNumbersKeepTheirUnits() {
        let nb = "\u{00A0}"
        XCTAssertEqual(EfficiencyLogic.keepUnits("Below 32 °F"), "Below 32\(nb)°F")
        XCTAssertEqual(EfficiencyLogic.keepUnits("86 °F and above"), "86\(nb)°F and above")
        XCTAssertEqual(EfficiencyLogic.keepUnits("0–10 °C"), "0–10\(nb)°C")
        XCTAssertEqual(EfficiencyLogic.keepUnits("55–70 mph"), "55–70\(nb)mph")
        XCTAssertEqual(EfficiencyLogic.keepUnits("70 mph and above"), "70\(nb)mph and above")
        XCTAssertEqual(EfficiencyLogic.keepUnits("89–113 km/h"), "89–113\(nb)km/h")
        XCTAssertEqual(EfficiencyLogic.keepUnits("All-Purpose"), "All-Purpose")
        XCTAssertEqual(EfficiencyLogic.keepUnits("2 miles"), "2 miles", "a word that starts like a unit is left alone")
    }

    func testVoiceOverSaysThinBestAndNoData() throws {
        let t = try fixture().byTemperature
        XCTAssertEqual(EfficiencyLogic.spoken(t[0], unit: "mi/kWh", best: false), "Below 32 °F: no data")
        XCTAssertTrue(EfficiencyLogic.spoken(t[4], unit: "mi/kWh", best: false).contains("thin"))
        let best = EfficiencyLogic.spoken(t[3], unit: "mi/kWh", best: true)
        XCTAssertTrue(best.hasSuffix(", best"), best)
        XCTAssertTrue(best.contains("miles per kilowatt hour"), best)
        XCTAssertFalse(best.contains("mi/kWh"))
    }

    /// A value that would sit on the dashed average line moves above it; others stay on their bar.
    func testValuesStayOffTheAverageLine() {
        XCTAssertEqual(EfficiencyLogic.valueBottom(bar: 50, average: 100, labelHeight: 16, gap: 2), 50)
        XCTAssertEqual(EfficiencyLogic.valueBottom(bar: 90, average: 100, labelHeight: 16, gap: 2), 102)
        XCTAssertEqual(EfficiencyLogic.valueBottom(bar: 101, average: 100, labelHeight: 16, gap: 2), 102)
        XCTAssertEqual(EfficiencyLogic.valueBottom(bar: 120, average: 100, labelHeight: 16, gap: 2), 120)
        XCTAssertEqual(EfficiencyLogic.valueBottom(bar: 90, average: nil, labelHeight: 16, gap: 2), 90)
    }

    private final class Fake: EfficiencySource, @unchecked Sendable {
        var result: Result<EfficiencyInsights?, Error>
        private(set) var calls: [String] = []
        init(_ r: Result<EfficiencyInsights?, Error>) { result = r }
        func efficiency(vehicleID: String, units: String, since: Date, until: Date) async throws -> EfficiencyInsights? {
            calls.append("\(vehicleID)|\(units)")
            return try result.get()
        }
    }

    func testTheModelCachesPerTruckMonthAndUnitsAndClears() async throws {
        let e = try fixture()
        let src = Fake(.success(e))
        let model = EfficiencyModel()
        let month = PeriodMonth(containing: Date(timeIntervalSince1970: 1_791_000_000), calendar: .current)
        let now = Date()
        await model.load(source: src, vehicleID: "veh-fixture", month: month, metric: false, now: now)
        XCTAssertEqual(model.shown, .insights(e))
        await model.load(source: src, vehicleID: "veh-fixture", month: month, metric: false, now: now.addingTimeInterval(60))
        XCTAssertEqual(src.calls, ["veh-fixture|imperial"])
        await model.load(source: src, vehicleID: "veh-fixture", month: month, metric: true, now: now)
        XCTAssertEqual(src.calls.last, "veh-fixture|metric")
        // An older server: the "not available" line.
        src.result = .success(nil)
        await model.load(source: src, vehicleID: "veh-other", month: month, metric: false, now: now)
        XCTAssertEqual(model.shown, .unavailable)
        // A failure for another truck never shows the last one's figures.
        src.result = .failure(APIError.server(status: 500, message: "no"))
        await model.load(source: src, vehicleID: "veh-third", month: month, metric: false, now: now)
        XCTAssertEqual(model.shown, .failed)
        model.clear()
        XCTAssertEqual(model.shown, .loading)
        src.result = .success(e)
        await model.load(source: src, vehicleID: "veh-fixture", month: month, metric: false, now: now)
        XCTAssertEqual(src.calls.filter { $0 == "veh-fixture|imperial" }.count, 2, "cleared, so read again")
    }
}

/// The request as sent: the truck and units through queryValue, the month's bounds; a 404 is "unavailable".
final class EfficiencyClientTests: XCTestCase {
    private final class Stub: URLProtocol {
        nonisolated(unsafe) static var calls: [String] = []
        nonisolated(unsafe) static var status = 200
        override class func canInit(with request: URLRequest) -> Bool { true }
        override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
        override func startLoading() {
            Self.calls.append(request.url.map { $0.path + "?" + ($0.query ?? "") } ?? "")
            let body = Self.status == 200 ? (try? Fixture.data("efficiency")) ?? Data() : Data("404 page not found".utf8)
            let resp = HTTPURLResponse(url: request.url!, statusCode: Self.status, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: body)
            client?.urlProtocolDidFinishLoading(self)
        }
        override func stopLoading() {}
    }

    private struct NoTokens: TokenStore {
        func token(for server: URL) throws -> String? { nil }
        func setToken(_ token: String, for server: URL) throws {}
        func deleteToken(for server: URL) throws {}
    }

    private func client() -> APIClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [Stub.self]
        return APIClient(baseURL: URL(string: "https://rivolt.example")!, tokens: NoTokens(), session: URLSession(configuration: config))
    }

    func testTheRequestAndAnOlderServer() async throws {
        Stub.calls = []
        Stub.status = 200
        let since = try XCTUnwrap(JSON.parseDate("2026-10-01T05:00:00Z")), until = try XCTUnwrap(JSON.parseDate("2026-11-01T05:00:00Z"))
        let e = try await client().efficiency(vehicleID: "veh fixture&x", units: "imperial", since: since, until: until)
        XCTAssertEqual(e?.total.drives, 7)
        XCTAssertEqual(Stub.calls, ["/api/insights/efficiency?vehicle=veh%20fixture%26x&units=imperial&since=2026-10-01T05:00:00Z&until=2026-11-01T05:00:00Z"])
        Stub.status = 404
        let none = try await client().efficiency(vehicleID: "veh-fixture", units: "metric", since: since, until: until)
        XCTAssertNil(none)
    }
}
