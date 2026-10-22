import XCTest
@testable import Rivolt

private func cal(_ zone: String) -> Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: zone)!
    return c
}

private func at(_ s: String) -> Date { JSON.parseDate(s)! }

private func charge(cost: Double, source: String?, derived: Bool? = nil, estimated: Double? = nil, currency: String = "USD",
                    kind: String? = "ac", place: String? = "home") -> ActivityCharge {
    ActivityCharge(id: "c", vehicleId: "v", startedAt: at("2026-09-01T10:00:00Z"), endedAt: at("2026-09-01T12:00:00Z"),
                   energyAddedKwh: 10, startSocPct: 40, endSocPct: 60, maxPowerKw: 7, cost: cost, currency: currency,
                   costSourceEffective: source, costSourceDerived: derived, estimatedCost: estimated, estimatedCurrency: "USD",
                   kind: kind, place: place, network: nil)
}

final class ActivityLogicTests: XCTestCase {
    func testTheFixtureDecodes() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        XCTAssertEqual(r.drives.count, 3)
        XCTAssertEqual(r.charges.count, 4)
        let merged = try XCTUnwrap(r.drives.first { $0.id == "drv-2a" })
        XCTAssertEqual(merged.roundTripLegs, 2)
        XCTAssertEqual(merged.drivingSeconds, 5010)
        XCTAssertNil(try XCTUnwrap(r.drives.first { $0.id == "drv-1" }).estimatedCost)
    }

    func testDaysAreNewestFirstAndGroupedByTheLocalDay() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let days = ActivityLogic.days(r, filter: .all, calendar: cal("UTC"))
        XCTAssertEqual(days.flatMap(\.items).count, 7)
        XCTAssertEqual(days.map(\.day), days.map(\.day).sorted(by: >))
        // Drive drv-2a (14:00Z) and charge chg-fast-1 (18:00Z) are the same UTC day, and a Tokyo phone moves the day.
        XCTAssertEqual(days.first { $0.items.contains(where: { $0.id == "drive:drv-2a" }) }?.items.count, 2)
        XCTAssertEqual(ActivityLogic.days(r, filter: .drives, calendar: cal("UTC")).flatMap(\.items).count, 3)
        XCTAssertEqual(ActivityLogic.days(r, filter: .charges, calendar: cal("UTC")).flatMap(\.items).count, 4)
        XCTAssertEqual(days.flatMap(\.items).map(\.startedAt), days.flatMap(\.items).map(\.startedAt).sorted(by: >))
    }

    func testTheMonthIsWholeLocalDaysWithAnExclusiveEnd() {
        let c = cal("America/Chicago")
        let m = ActivityMonth(containing: at("2026-10-21T15:00:00Z"), calendar: c)
        let r = m.range(calendar: c)
        XCTAssertEqual(r.since, at("2026-10-01T05:00:00Z"))
        XCTAssertEqual(r.until, at("2026-11-01T05:00:00Z"))
        XCTAssertTrue(m.isCurrent(now: at("2026-10-31T23:00:00Z"), calendar: c))
        XCTAssertFalse(m.shifted(by: -1, calendar: c).isCurrent(now: at("2026-10-21T15:00:00Z"), calendar: c))
        XCTAssertEqual(m.shifted(by: -1, calendar: c).range(calendar: c).until, r.since)
    }

    func testPlaceLabelsFollowTheWebsRules() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let labels = Dictionary(uniqueKeysWithValues: r.charges.map { ($0.id, ActivityLogic.placeLabel($0)) })
        XCTAssertEqual(labels["chg-home-2"], "Home")
        XCTAssertEqual(labels["chg-away-1"], "Away")
        XCTAssertEqual(labels["chg-fast-1"], "Fast")
        XCTAssertNil(try XCTUnwrap(labels["chg-legacy-1"], "the legacy charge is in the fixture"))
    }

    func testCostLinesFollowTheLedgersSource() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let byID = Dictionary(uniqueKeysWithValues: r.charges.map { ($0.id, ActivityLogic.costLine($0)) })
        XCTAssertEqual(byID["chg-home-2"], ChargeCostLine(text: "$5.21", tone: .billed))
        XCTAssertEqual(byID["chg-fast-1"], ChargeCostLine(text: "$23.41", tone: .billed))
        XCTAssertEqual(byID["chg-away-1"], ChargeCostLine(text: "Unpriced", tone: .missing))
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 20, source: "network_estimate")).tone, .estimate)
        XCTAssertTrue(ActivityLogic.costLine(charge(cost: 20, source: "network_estimate")).text.hasSuffix("(estimate)"))
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 0, source: "network_estimate")).tone, .missing)
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 4, source: "home_rate", derived: true)).tone, .estimate)
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 4, source: "home_rate")).tone, .billed)
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 0, source: "user")), ChargeCostLine(text: "Free", tone: .free))
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 8, source: "home_rate_misapplied")).text, "Estimate pending")
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 0, source: "rivian", estimated: 3.2)).tone, .estimate)
        // A server that predates the source: whatever it priced, else unpriced.
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 6, source: nil)).tone, .billed)
        XCTAssertEqual(ActivityLogic.costLine(charge(cost: 0, source: nil)).tone, .missing)
        // No currency reads as dollars, as on the web, never as a bare number.
        XCTAssertTrue(ActivityLogic.costLine(charge(cost: 6, source: "rivian", currency: "")).text.contains("$"))
    }

    func testADriveWithoutEnergyIsFlaggedNotGivenAnEfficiency() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let d = try XCTUnwrap(r.drives.first)
        XCTAssertFalse(ActivityLogic.missingEnergy(d))
        XCTAssertEqual(try XCTUnwrap(ActivityLogic.efficiency(d)), 18.6 / 6.9, accuracy: 0.0001)
        let none = ActivityDrive(id: "x", vehicleId: "v", startedAt: .now, endedAt: .now.addingTimeInterval(600), distanceMi: 5, energyUsedKwh: 0,
                                 startSocPct: 0, endSocPct: 0, avgSpeedMph: 30, maxSpeedMph: 40, estimatedCost: 2, estimatedCurrency: "USD",
                                 roundTripLegs: nil, drivingSeconds: nil)
        XCTAssertTrue(ActivityLogic.missingEnergy(none))
        XCTAssertNil(ActivityLogic.efficiency(none))
        XCTAssertTrue(ActivityLogic.spoken(none, calendar: cal("UTC")).contains("No energy reading"))
        XCTAssertFalse(ActivityLogic.spoken(none, calendar: cal("UTC")).contains("estimate"))
    }

    func testAMergedRoundTripCountsItsDrivingTimeNotTheTimeParked() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let merged = try XCTUnwrap(r.drives.first { $0.id == "drv-2a" })
        XCTAssertEqual(ActivityLogic.duration(merged), 5010)
        let plain = try XCTUnwrap(r.drives.first { $0.id == "drv-1" })
        XCTAssertEqual(ActivityLogic.duration(plain), 62 * 60)
    }

    func testSpokenRowsUseWordsNotSymbols() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let c = cal("UTC")
        let said = ActivityLogic.spoken(try XCTUnwrap(r.drives.first), calendar: c)
        XCTAssertTrue(said.contains("an estimate"), said)
        XCTAssertFalse(said.contains("~"), said)
        let est = ActivityLogic.spoken(charge(cost: 20, source: "network_estimate"), calendar: c)
        XCTAssertTrue(est.contains("an estimate"), est)
        XCTAssertFalse(est.contains("~"), est)
        XCTAssertTrue(ActivityLogic.spoken(try XCTUnwrap(r.charges.first { $0.id == "chg-fast-1" }), calendar: c).contains("Fast charger"))
    }
}

private actor Counter {
    var calls = 0
    func bump() { calls += 1 }
}

private struct FakeActivity: ActivitySource {
    let counter = Counter()
    var result: Result<ActivityResponse, Error>
    func activity(since: Date, until: Date, vehicleID: String) async throws -> ActivityResponse {
        await counter.bump()
        return try result.get()
    }
}

@MainActor
final class ActivityModelTests: XCTestCase {
    private let month = ActivityMonth(containing: at("2026-09-15T00:00:00Z"), calendar: cal("UTC"))
    private func sample() throws -> ActivityResponse { try Fixture.decode(ActivityResponse.self, "activity") }

    func testAnAnswerIsReusedForFifteenMinutesAndAPullReadsAgain() async throws {
        let src = FakeActivity(result: .success(try sample()))
        let model = ActivityModel()
        let t0 = at("2026-10-21T15:00:00Z")
        await model.load(source: src, vehicleID: "a", month: month, now: t0, calendar: cal("UTC"))
        await model.load(source: src, vehicleID: "a", month: month, now: t0.addingTimeInterval(14 * 60), calendar: cal("UTC"))
        var n = await src.counter.calls
        XCTAssertEqual(n, 1)
        await model.load(source: src, vehicleID: "a", month: month.shifted(by: -1, calendar: cal("UTC")), now: t0, calendar: cal("UTC"))
        await model.load(source: src, vehicleID: "b", month: month, now: t0, calendar: cal("UTC"))
        n = await src.counter.calls
        XCTAssertEqual(n, 3)
        await model.load(source: src, vehicleID: "a", month: month, force: true, now: t0.addingTimeInterval(14 * 60), calendar: cal("UTC"))
        n = await src.counter.calls
        XCTAssertEqual(n, 4)
    }

    func testARefusedReadKeepsWhatIsOnScreenAndSaysSlowDown() async throws {
        let model = ActivityModel()
        await model.load(source: FakeActivity(result: .success(try sample())), vehicleID: "a", month: month, calendar: cal("UTC"))
        let limited = FakeActivity(result: .failure(APIError.server(status: 429, message: "slow down")))
        await model.load(source: limited, vehicleID: "a", month: month, force: true, calendar: cal("UTC"))
        XCTAssertEqual(model.response?.charges.count, 4)
        XCTAssertTrue(model.limited)
        XCTAssertTrue(model.failed)
    }

    func testAnotherMonthNeverShowsThisMonthsRows() async throws {
        let model = ActivityModel()
        await model.load(source: FakeActivity(result: .success(try sample())), vehicleID: "a", month: month, calendar: cal("UTC"))
        await model.load(source: FakeActivity(result: .failure(APIError.transport("down"))), vehicleID: "a",
                         month: month.shifted(by: -1, calendar: cal("UTC")), calendar: cal("UTC"))
        XCTAssertNil(model.response)
        XCTAssertTrue(model.failed)
        XCTAssertFalse(model.limited)
    }
}
