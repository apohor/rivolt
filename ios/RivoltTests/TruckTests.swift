import XCTest
@testable import Rivolt

private func chicago() -> Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: "America/Chicago")!
    c.locale = Locale(identifier: "en_US")
    return c
}

private func at(_ s: String) -> Date { JSON.parseDate(s)! }

final class TruckLogicTests: XCTestCase {
    private func drain() throws -> DrainSnapshot { try Fixture.decode(DrainSnapshot.self, "drain") }

    func testTheSharedFixturesDecode() throws {
        let d = try drain()
        XCTAssertEqual(d.windows.week.drainPctPerDay, 0.6)
        XCTAssertEqual(d.windows.month.split?.reason, "not_enough_data")
        XCTAssertEqual(d.daily.count, 30)
        XCTAssertEqual(try Fixture.decode(PackHealth.self, "pack_health").samples?.count, 5)
        XCTAssertEqual(try Fixture.decode(TruckTrends.self, "trends").capacity?.medianKwh, 122.4)
        XCTAssertEqual(try Fixture.decode(ColdRange.self, "cold_range").available, [4.4, 15.6, 26.7])
        XCTAssertEqual(try Fixture.decode(LeaseStatus.self, "lease_status").daysLeft, 588)
        XCTAssertEqual(try Fixture.decode([Vehicle].self, "vehicles")[0].ota?.current?.version, "2026.34.1")
    }

    func testTheStateIsDecodedWithoutItsCoordinates() throws {
        let s = try Fixture.decode(VehicleState.self, "state")
        let fields = Mirror(reflecting: s).children.compactMap(\.label).map { $0.lowercased() }
        XCTAssertFalse(fields.contains { $0.contains("lat") || $0.contains("lon") || $0.contains("heading") || $0.contains("altitude") })
        XCTAssertEqual(s.otaCurrentVersion, "2026.34.1")
    }

    func testParkedDrainReadsAsTheWebs() throws {
        let d = try drain()
        let v = TruckLogic.drainView(d.windows.week, .week)
        XCTAssertEqual(v, .ok(pctPerDay: 0.6, kwhPerDay: 0.8, sleeps: 22, quickRewakes: 0, medianSleepSeconds: 12240, daysUsed: 7, daysExcluded: 3))
        XCTAssertEqual(TruckLogic.drainSleeps(.week, sleeps: 22, quickRewakes: 0, medianSleepSeconds: 12240),
                       "Over 7 days: 22 sleeps, median 3.4 h. Time spent awake while parked is in the Sleep card below.")
        XCTAssertEqual(TruckLogic.drainBasis(daysUsed: 7, daysExcluded: 3, other: .month, otherView: TruckLogic.drainView(d.windows.month, .month)),
                       "Based on 7 days (3 left out: too little parked data). 30 days: 1.5% / day.")
        XCTAssertEqual(TruckLogic.split(d, .week),
                       .ok(text: "Of the 3% your truck lost while parked this week, about 0% was while it was awake (6.0 h).", awakeShare: 0.06, awake: true, asleep: true))
        // 30 days is too thin for the split, so the week's is shown, said as such.
        if case .ok(let text, _, _, _)? = TruckLogic.split(d, .month) { XCTAssertTrue(text.contains("this week")) } else { XCTFail("expected the week's split") }
    }

    func testThinDrainDataNeverGivesANumber() throws {
        let thin = DrainWindow(drainPctPerDay: 2, drainKwhPerDay: nil, sleeps: 1, quickRewakes: 0, medianSleepSeconds: nil, parkedHours: 9.5, daysWithData: 1,
                               daysExcluded: nil, sufficient: false, split: nil)
        XCTAssertEqual(TruckLogic.drainView(thin, .month), .insufficient(haveHours: 9.5, needHours: 72))
        XCTAssertEqual(TruckLogic.hours(9.5), "9.5 h")
        XCTAssertEqual(TruckLogic.hours(0.5), "30 min")
    }

    func testDrainBarsKeepLeftOutDaysAndNeverFakeAZero() throws {
        let (bars, top) = TruckLogic.drainBars(try drain().daily)
        XCTAssertEqual(top, 3.9)
        XCTAssertEqual(bars.count, 30)
        XCTAssertNil(bars[2].value)
        XCTAssertEqual(bars[2].excluded, "insufficient")
        XCTAssertEqual(bars[14].height, 1)
        let rose = try XCTUnwrap(bars.first { $0.gained != nil })
        XCTAssertEqual(rose.gained, 0.3)
        XCTAssertEqual(rose.value, 0)
    }

    func testPackCapacityPrefersTheTrucksOwnFigureAndHidesAGrossSpec() throws {
        let v = TruckLogic.pack(try Fixture.decode(PackHealth.self, "pack_health"))
        XCTAssertEqual(v.head, .reported(kwh: 123, spec: 135))
        XCTAssertEqual(v.trend.count, 5)
        XCTAssertEqual(v.note, "Median of the last 5 clean sessions · 5 total")
        XCTAssertEqual(TruckLogic.specKwh(180), 0)
    }

    func testTrendsSayHowFarAlongTheyAre() throws {
        let t = try Fixture.decode(TruckTrends.self, "trends")
        XCTAssertEqual(TruckLogic.capacity(t), .ok(median: 122.4, spec: 135, need: 30))
        XCTAssertEqual(TruckLogic.monthLabel("2026-08"), "Aug 26")
    }

    func testRangeChipsAndBands() throws {
        let d = try Fixture.decode(ColdRange.self, "cold_range")
        XCTAssertEqual(TruckLogic.grid(fahrenheit: true).map { TruckLogic.chip($0, fahrenheit: true) }, ["0 °F", "20 °F", "40 °F", "60 °F", "80 °F"])
        XCTAssertEqual(TruckLogic.grid(fahrenheit: false).map { TruckLogic.chip($0, fahrenheit: false) }, ["-20 °C", "-10 °C", "0 °C", "10 °C", "20 °C"])
        XCTAssertTrue(TruckLogic.chipOff(d, -17.8))
        XCTAssertFalse(TruckLogic.chipOff(d, 15.6))
        XCTAssertEqual(TruckLogic.band(283, 283, digits: 0, unit: "mi"), "283 mi")
        XCTAssertEqual(TruckLogic.band(2.25, 2.41, digits: 1, unit: "mi/kWh"), "2.3–2.4 mi/kWh")
        XCTAssertEqual(TruckLogic.temperature(11.7, fahrenheit: true), "53 °F")
    }

    func testTheLeaseBarAndStanding() throws {
        let st = try Fixture.decode(LeaseStatus.self, "lease_status")
        let b = TruckLogic.leaseBar(st, allowedTotal: 36000)
        XCTAssertEqual(b.used, 14520.2 / 36000, accuracy: 1e-9)
        XCTAssertEqual(b.plan, 15880.3 / 36000, accuracy: 1e-9)
        XCTAssertEqual(b.allowed, 1)
        XCTAssertEqual(b.tone, .ok)
        XCTAssertEqual(TruckLogic.planStanding(st, metric: false)?.text, "1,360 mi under plan")
        XCTAssertEqual(TruckLogic.planStanding(st, metric: true)?.text, "2,189 km under plan")
        XCTAssertEqual(TruckLogic.distance(36000, metric: false), "36,000 mi")
    }

    func testTheSleepWindowIsTheWebs() {
        let cal = chicago()
        let oct = PeriodMonth(containing: at("2026-10-15T17:00:00Z"), calendar: cal)
        let w = TruckLogic.sleepWindow(oct, now: at("2026-10-22T17:00:00Z"), calendar: cal)
        XCTAssertEqual(w.since, at("2026-10-01T00:00:00Z"))
        XCTAssertEqual(w.until, at("2026-10-23T00:00:00Z"))
        XCTAssertFalse(w.capped)
    }

    func testSleepTroubleNeedsThreeFailedSleepsInTwoDays() {
        func day(_ s: String, failed: Int, sleeps: Int) -> DayActivity { DayActivity(day: at(s), asleepH: 10, idleAwakeH: 1.5, sleeps: sleeps, failedSleeps: failed) }
        let cal = chicago()
        let ok = TruckLogic.sleepSummary([day("2026-10-20T00:00:00Z", failed: 1, sleeps: 4), day("2026-10-21T00:00:00Z", failed: 1, sleeps: 5)], now: at("2026-10-22T17:00:00Z"), calendar: cal)
        XCTAssertNil(ok.trouble)
        XCTAssertEqual(ok.averageDays, 2)
        XCTAssertEqual(ok.average, 90)
        let bad = TruckLogic.sleepSummary([day("2026-10-20T00:00:00Z", failed: 2, sleeps: 4), day("2026-10-21T00:00:00Z", failed: 2, sleeps: 5)], now: at("2026-10-22T17:00:00Z"), calendar: cal)
        XCTAssertEqual(bad.trouble?.failed, 4)
        XCTAssertEqual(bad.trouble?.sleeps, 9)
        XCTAssertEqual(TruckLogic.minutes(395), "6 h 35 min")
        XCTAssertEqual(TruckLogic.minutesShort(1022), "17h 2m")
        XCTAssertEqual(TruckLogic.minutesShort(0), "0m")
    }

    func testTiresSoftwareAndTheNotesLink() throws {
        let s = try Fixture.decode(VehicleState.self, "state")
        let tire = try XCTUnwrap(TruckLogic.lowestTire(s))
        XCTAssertEqual(tire.bar, 2.9)
        XCTAssertEqual(TruckLogic.fixed(tire.psi, 0), "42")
        XCTAssertFalse(tire.low)
        let sw = try XCTUnwrap(TruckLogic.software(s))
        XCTAssertEqual(sw.current, "2026.34.1")
        XCTAssertNil(sw.available)
        XCTAssertNotNil(TruckLogic.notesURL("https://rivian.com/support/notes"))
        XCTAssertNil(TruckLogic.notesURL("http://rivian.com/support/notes"))
        XCTAssertNil(TruckLogic.notesURL("https://user:pw@rivian.com/notes"))
        XCTAssertNil(TruckLogic.notesURL("javascript:alert(1)"))
        XCTAssertTrue(TruckLogic.isR2(" r2 "))
    }
}

/// A fake server: answers from the fixtures, counts the calls, or fails with the status given.
private final class FakeTruck: TruckSource, @unchecked Sendable {
    var calls: [String] = []
    var fail: Int?
    var lease = true

    private func check(_ name: String) throws {
        calls.append(name)
        if let fail { throw APIError.server(status: fail, message: "no") }
    }

    func vehicleState(id: String) async throws -> VehicleState { try check("state"); return try Fixture.decode(VehicleState.self, "state") }
    func drain(vehicleID: String) async throws -> DrainSnapshot? { try check("drain"); return try Fixture.decode(DrainSnapshot.self, "drain") }
    func packHealth(vehicleID: String) async throws -> PackHealth { try check("pack"); return try Fixture.decode(PackHealth.self, "pack_health") }
    func trends(vehicleID: String, metric: Bool) async throws -> TruckTrends? { try check("trends"); return nil }
    func coldRange(vehicleID: String, tempC: Double, grid: [Double], metric: Bool) async throws -> ColdRange? {
        try check("cold \(tempC)"); return try Fixture.decode(ColdRange.self, "cold_range")
    }
    func leaseTerms(vehicleID: String) async throws -> LeaseTerms? { try check("lease"); return lease ? try Fixture.decode(LeaseTerms.self, "lease_terms") : nil }
    func leaseStatus(vehicleID: String) async throws -> LeaseStatus? { try check("leaseStatus"); return try Fixture.decode(LeaseStatus.self, "lease_status") }
    func sleepActivity(since: Date, until: Date, vehicleID: String) async throws -> [DayActivity] {
        try check("sleep"); return try Fixture.decode([DayActivity].self, "sleep_activity")
    }
}

@MainActor
final class TruckModelTests: XCTestCase {
    private let now = at("2026-10-22T17:00:00Z")

    func testCardsAreReadOnceIn15MinutesAndA429KeepsThem() async {
        let src = FakeTruck()
        let model = TruckModel()
        await model.load(source: src, vehicleID: "v1", metric: false, now: now)
        XCTAssertEqual(Set(src.calls), ["state", "drain", "pack", "trends", "lease", "leaseStatus"])
        XCTAssertNotNil(model.drain?.value)
        // No trends on this server: the card is left out, not an error.
        XCTAssertEqual(model.trends, .missing)
        XCTAssertNotNil(model.lease?.value)

        src.calls = []
        await model.load(source: src, vehicleID: "v1", metric: false, now: now.addingTimeInterval(14 * 60))
        XCTAssertEqual(src.calls, [])

        src.fail = 429
        await model.load(source: src, vehicleID: "v1", metric: false, force: true, now: now.addingTimeInterval(20 * 60))
        XCTAssertNotNil(model.drain?.value)
        XCTAssertNotNil(model.pack?.value)
        XCTAssertNotNil(model.state)

        // Another truck never shows the first one's figures.
        await model.load(source: src, vehicleID: "v2", metric: false, now: now.addingTimeInterval(21 * 60))
        XCTAssertEqual(model.drain, .failed)
        XCTAssertNil(model.state)
    }

    func testTheLeaseStatusIsOnlyAskedWithTerms() async {
        let src = FakeTruck()
        src.lease = false
        let model = TruckModel()
        await model.load(source: src, vehicleID: "v1", metric: false, now: now)
        XCTAssertFalse(src.calls.contains("leaseStatus"))
        XCTAssertEqual(model.lease, .missing)
        XCTAssertNil(model.leaseTerms)
    }

    func testSleepAndRangeFollowTheMonthAndTemperature() async {
        let src = FakeTruck()
        let model = TruckModel()
        let cal = chicago()
        await model.loadSleep(source: src, vehicleID: "v1", month: PeriodMonth(containing: now, calendar: cal), now: now, calendar: cal)
        XCTAssertNotNil(model.sleep?.value)
        await model.loadCold(source: src, vehicleID: "v1", tempC: 4.4, fahrenheit: true, metric: false, now: now)
        await model.loadCold(source: src, vehicleID: "v1", tempC: 4.4, fahrenheit: true, metric: false, now: now)
        XCTAssertEqual(src.calls.filter { $0.hasPrefix("cold") }, ["cold 4.4"])
        await model.loadCold(source: src, vehicleID: "v1", tempC: 15.6, fahrenheit: true, metric: false, now: now)
        XCTAssertEqual(src.calls.filter { $0.hasPrefix("cold") }, ["cold 4.4", "cold 15.6"])
    }
}

/// Answers the client in place of the server, by path.
private final class TruckStub: URLProtocol {
    nonisolated(unsafe) static var paths: [String] = []
    nonisolated(unsafe) static var respond: (String) -> (Int, Data) = { _ in (500, Data()) }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let path = request.url.map { $0.path + ($0.query.map { "?" + $0 } ?? "") } ?? ""
        Self.paths.append(path)
        let (status, data) = Self.respond(path)
        let resp = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private struct TruckNoTokens: TokenStore {
    func token(for server: URL) throws -> String? { nil }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

final class TruckClientTests: XCTestCase {
    private func client() -> APIClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [TruckStub.self]
        return APIClient(baseURL: URL(string: "https://rivolt.example")!, tokens: TruckNoTokens(), session: URLSession(configuration: config))
    }

    override func setUp() {
        super.setUp()
        TruckStub.paths = []
    }

    func testTheReadsAreTheWebsAndA404LeavesTheCardOut() async throws {
        TruckStub.respond = { path in
            if path.hasSuffix("/drain") { return (200, (try? Fixture.data("drain")) ?? Data()) }
            if path.contains("/cold-range") { return (200, (try? Fixture.data("cold_range")) ?? Data()) }
            return (404, Data("404 page not found".utf8))
        }
        let c = client()
        let d = try await c.drain(vehicleID: "veh-fixture")
        XCTAssertEqual(d?.windows.week.sleeps, 22)
        let r = try await c.coldRange(vehicleID: "veh-fixture", tempC: 15.6, grid: [-17.8, -6.7, 4.4, 15.6, 26.7], metric: false)
        XCTAssertEqual(r?.rangeLow, 283)
        let t = try await c.trends(vehicleID: "veh-fixture", metric: true)
        XCTAssertNil(t)
        let l = try await c.leaseTerms(vehicleID: "veh-fixture")
        XCTAssertNil(l)
        XCTAssertEqual(TruckStub.paths, [
            "/api/vehicles/veh-fixture/drain",
            "/api/vehicles/veh-fixture/cold-range?temp_c=15.6&grid=-17.8,-6.7,4.4,15.6,26.7&units=imperial",
            "/api/vehicles/veh-fixture/trends?units=metric",
            "/api/vehicles/veh-fixture/lease",
        ])
    }

    func testARateLimitIsAnErrorNotAMissingCard() async {
        TruckStub.respond = { _ in (429, Data("{\"error\":\"slow down\"}".utf8)) }
        do {
            _ = try await client().drain(vehicleID: "v")
            XCTFail("expected the 429")
        } catch APIError.server(let status, _) {
            XCTAssertEqual(status, 429)
        } catch {
            XCTFail("unexpected \(error)")
        }
    }
}
