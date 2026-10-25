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

final class EnergyEstimatedTests: XCTestCase {
    private func make(estimated: Bool?, kwh: Double = 12.34, cost: Double = 1.85, source: String? = "home_rate") -> ActivityCharge {
        var c = charge(cost: cost, source: source)
        c = ActivityCharge(id: c.id, vehicleId: c.vehicleId, startedAt: c.startedAt, endedAt: c.endedAt, energyAddedKwh: kwh, startSocPct: 40, endSocPct: 60,
                           maxPowerKw: 7, cost: cost, currency: "USD", costSourceEffective: source, costSourceDerived: nil, estimatedCost: nil,
                           estimatedCurrency: nil, kind: "ac", place: "home", network: nil, energyEstimated: estimated)
        return c
    }

    func testTheFlagDecodesFromTheActivityFeedAndIsAbsentOnAnOlderServer() throws {
        let json = #"{"id":"c","vehicle_id":"v","started_at":"2026-10-20T01:00:00Z","ended_at":"2026-10-20T05:00:00Z","energy_added_kwh":12.3,"start_soc_pct":40,"end_soc_pct":60,"max_power_kw":7,"cost":1.85,"currency":"USD","energy_estimated":true}"#
        XCTAssertTrue(try JSON.decoder().decode(ActivityCharge.self, from: Data(json.utf8)).isEnergyEstimated)
        let old = json.replacingOccurrences(of: #","energy_estimated":true"#, with: "")
        XCTAssertFalse(try JSON.decoder().decode(ActivityCharge.self, from: Data(old.utf8)).isEnergyEstimated)
    }

    func testEstimatedEnergyAndCostAreMarkedAndNothingElseIs() {
        XCTAssertEqual(ActivityLogic.energyText(make(estimated: true)), "~12.3 kWh")
        XCTAssertEqual(ActivityLogic.energyText(make(estimated: nil)), "12.3 kWh")
        XCTAssertEqual(ActivityLogic.energyText(make(estimated: false)), "12.3 kWh")
        XCTAssertEqual(ActivityLogic.costLine(make(estimated: true)).tone, .estimate)
        XCTAssertTrue(ActivityLogic.costLine(make(estimated: true)).text.hasPrefix("~$1.85"))
        XCTAssertEqual(ActivityLogic.costLine(make(estimated: nil)).tone, .billed)
        // Free and unpriced charges have no cost to mark; a network estimate is not marked twice.
        XCTAssertEqual(ActivityLogic.costLine(make(estimated: true, cost: 0, source: "user")).tone, .free)
        XCTAssertEqual(ActivityLogic.costLine(make(estimated: true, cost: 0, source: "unknown")).tone, .missing)
        XCTAssertEqual(ActivityLogic.costLine(make(estimated: true, source: "network_estimate")).text.filter { $0 == "~" }.count, 1)
    }

    func testTheTileAndTheSpokenWordsSayEstimateAndNeverMetered() {
        let c = make(estimated: true)
        let tile = ActivityLogic.tiles(c).first { $0.id == "energy" }
        XCTAssertEqual(tile?.value, "~12.3")
        XCTAssertEqual(tile?.hint, "Estimated from the battery's change. A Rivolt bug lost the energy count for this charge.")
        XCTAssertTrue(ActivityLogic.spoken(c, calendar: .current).contains("About 12.3 kilowatt hours added, an estimate."))
        XCTAssertNil(ActivityLogic.tiles(make(estimated: nil)).first { $0.id == "energy" }?.hint)
        XCTAssertNil(ActivityLogic.energyEstimatedNote.range(of: "exact|metered|measured|accurate", options: [.regularExpression, .caseInsensitive]))
    }
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
                                 startSocPct: 0, endSocPct: 0, avgSpeedMph: 30, maxSpeedMph: 40, estimatedCost: 2, estimatedCurrency: "USD", estimatedPricePerKwh: nil,
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

/// Busy the first time, fine the second.
private struct BusyThenFine: ActivitySource {
    let counter = Counter()
    let answer: ActivityResponse
    func activity(since: Date, until: Date, vehicleID: String) async throws -> ActivityResponse {
        await counter.bump()
        if await counter.calls == 1 { throw APIError.server(status: 429, message: "slow down") }
        return answer
    }
}

/// An answer that takes a moment, so two asks overlap.
private struct SlowActivity: ActivitySource {
    let counter = Counter()
    let answer: ActivityResponse
    func activity(since: Date, until: Date, vehicleID: String) async throws -> ActivityResponse {
        await counter.bump()
        try await Task.sleep(for: .milliseconds(150))
        return answer
    }
}

@MainActor
final class ActivityModelTests: XCTestCase {
    private let month = PeriodMonth(containing: at("2026-09-15T00:00:00Z"), calendar: cal("UTC"))
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
        await model.load(source: limited, vehicleID: "a", month: month, force: true, calendar: cal("UTC"), busyRetryAfter: .zero)
        XCTAssertEqual(model.response?.charges.count, 4)
        XCTAssertTrue(model.limited)
        XCTAssertTrue(model.failed)
        let tried = await limited.counter.calls
        XCTAssertEqual(tried, 2, "busy is asked again once, after a moment")
        XCTAssertNil(model.problem, "busy is not 'couldn't load this month'")
    }

    func testABusyServerThatRecoversOnTheSecondAskShowsTheMonth() async throws {
        let src = BusyThenFine(answer: try sample())
        let model = ActivityModel()
        await model.load(source: src, vehicleID: "a", month: month, calendar: cal("UTC"), busyRetryAfter: .zero)
        XCTAssertEqual(model.response?.charges.count, 4)
        XCTAssertFalse(model.failed)
        XCTAssertFalse(model.limited)
        let n = await src.counter.calls
        XCTAssertEqual(n, 2)
    }

    func testTwoAsksForTheSameMonthAtOnceShareOneRead() async throws {
        let src = SlowActivity(answer: try sample())
        let model = ActivityModel()
        async let a: Void = model.load(source: src, vehicleID: "a", month: month, calendar: cal("UTC"))
        async let b: Void = model.load(source: src, vehicleID: "a", month: month, force: true, calendar: cal("UTC"))
        _ = await (a, b)
        let n = await src.counter.calls
        XCTAssertEqual(n, 1, "a refresh landing with the appear read does not spend the allowance twice")
        XCTAssertEqual(model.response?.charges.count, 4)
        // Another month is its own read.
        await model.load(source: src, vehicleID: "a", month: month.shifted(by: -1, calendar: cal("UTC")), calendar: cal("UTC"))
        let m = await src.counter.calls
        XCTAssertEqual(m, 2)
    }

    func testTheDemosLiveAnswerReadsAndFillsTheMonth() async throws {
        // Trimmed from the demo server's own answer: microsecond timestamps, a null trailer correction, AC and fast charges.
        let live = try Fixture.decode(ActivityResponse.self, "activity_live_shape")
        XCTAssertEqual(live.drives.count, 3)
        XCTAssertEqual(live.charges.count, 3)
        XCTAssertNotNil(live.drives.first?.roundTripLegs)
        XCTAssertEqual(live.charges.first?.kind, "ac")
        let model = ActivityModel()
        await model.load(source: FakeActivity(result: .success(live)), vehicleID: "a", month: month, calendar: cal("UTC"))
        XCTAssertEqual(model.response?.drives.count, 3)
        XCTAssertFalse(model.failed)
        XCTAssertNil(model.problem)
    }

    func testAFailureSaysWhatKindItWasAndOnlyADroppedReadIsTriedAgain() async throws {
        let calendar = cal("UTC")
        let down = FakeActivity(result: .failure(URLError(.notConnectedToInternet)))
        let a = ActivityModel()
        await a.load(source: down, vehicleID: "a", month: month, calendar: calendar, retryAfter: .zero)
        XCTAssertEqual(a.problem, "Couldn't reach Rivolt. Check the connection, then pull to try again.")
        let calls = await down.counter.calls
        XCTAssertEqual(calls, 2, "a dropped connection is tried once more")
        let b = ActivityModel()
        let unreadable = FakeActivity(result: .failure(APIError.decoding(URLError(.cannotParseResponse))))
        await b.load(source: unreadable, vehicleID: "a", month: month, calendar: calendar, retryAfter: .zero)
        XCTAssertEqual(b.problem, "Rivolt couldn't read this month's answer. Updating the app may help.")
        let unreadableCalls = await unreadable.counter.calls
        XCTAssertEqual(unreadableCalls, 1, "an unreadable answer would read the same again")
        let c = ActivityModel()
        await c.load(source: FakeActivity(result: .failure(APIError.server(status: 503, message: "x"))), vehicleID: "a", month: month, calendar: calendar, retryAfter: .zero)
        XCTAssertEqual(c.problem, "Rivolt isn't answering right now. Pull to try again.")
    }

    func testAReadThatWasCancelledIsNotAFailureOfTheMonth() async throws {
        let model = ActivityModel()
        await model.load(source: FakeActivity(result: .failure(URLError(.cancelled))), vehicleID: "a", month: month, calendar: cal("UTC"), retryAfter: .zero)
        XCTAssertFalse(model.failed)
        XCTAssertNil(model.problem)
    }

    func testAnotherMonthNeverShowsThisMonthsRows() async throws {
        let model = ActivityModel()
        await model.load(source: FakeActivity(result: .success(try sample())), vehicleID: "a", month: month, calendar: cal("UTC"))
        await model.load(source: FakeActivity(result: .failure(APIError.transport("down"))), vehicleID: "a",
                         month: month.shifted(by: -1, calendar: cal("UTC")), calendar: cal("UTC"), retryAfter: .zero)
        XCTAssertNil(model.response)
        XCTAssertTrue(model.failed)
        XCTAssertFalse(model.limited)
    }
}

final class ActivityWordingTests: XCTestCase {
    func testTheFeedsTitlesAndTimes() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let utc = cal("UTC")
        let byID = Dictionary(uniqueKeysWithValues: r.charges.map { ($0.id, $0) })
        XCTAssertEqual(ActivityLogic.chargeTitle(try XCTUnwrap(byID["chg-home-2"])), "Home charge")
        XCTAssertEqual(ActivityLogic.chargeTitle(try XCTUnwrap(byID["chg-away-1"])), "Away charge")
        XCTAssertEqual(ActivityLogic.chargeTitle(try XCTUnwrap(byID["chg-fast-1"])), "Fast charge")
        XCTAssertEqual(ActivityLogic.chargeTitle(try XCTUnwrap(byID["chg-legacy-1"])), "Charge")
        XCTAssertEqual(ActivityLogic.levelChange(start: 79, end: 80), "79→80%")
        XCTAssertNil(ActivityLogic.levelChange(start: 0, end: 80))
        XCTAssertEqual(ActivityLogic.shortDuration(26 * 60), "26m")
        XCTAssertEqual(ActivityLogic.shortDuration(3 * 3600 + 32 * 60), "3h 32m")
        let t = ActivityLogic.timeRange(start: at("2026-10-21T19:48:00Z"), end: at("2026-10-21T20:14:00Z"), calendar: utc)
        XCTAssertTrue(t.contains("–"), t)
        XCTAssertEqual(ActivityLogic.timeRange(start: at("2026-10-21T19:48:00Z"), end: at("0001-01-01T00:00:00Z"), calendar: utc),
                       ActivityLogic.timeOfDay(at("2026-10-21T19:48:00Z"), calendar: utc), "an unset end shows only the start")
    }

    func testTheDriveTilesAreTheWebsAndCarryNoPlace() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let d = try XCTUnwrap(r.drives.first { $0.id == "drv-3" })
        let tiles = Dictionary(uniqueKeysWithValues: ActivityLogic.tiles(d, metric: false).map { ($0.id, $0) })
        XCTAssertEqual(tiles["distance"]?.value, "18.6")
        XCTAssertEqual(tiles["distance"]?.unit, "mi")
        XCTAssertEqual(tiles["duration"]?.value, "1h 5m")
        XCTAssertEqual(tiles["soc"]?.value, "85% → 80%")
        XCTAssertEqual(tiles["speed"]?.value, "31 / 58")
        XCTAssertEqual(tiles["energy"]?.value, "6.9")
        XCTAssertEqual(tiles["cost"]?.value, "~$0.90")
        XCTAssertEqual(tiles["cost"]?.hint, "at ~0.130 USD/kWh from your most recent charge")
        XCTAssertEqual(tiles["efficiency"]?.unit, "mi/kWh")
        let km = ActivityLogic.tiles(d, metric: true).first { $0.id == "distance" }
        XCTAssertEqual(km?.unit, "km")
    }

    func testAMissingEnergyShowsADashAndNoCostOrEfficiency() {
        let d = ActivityDrive(id: "x", vehicleId: "v", startedAt: .now, endedAt: .now.addingTimeInterval(600), distanceMi: 5, energyUsedKwh: 0,
                              startSocPct: 0, endSocPct: 0, avgSpeedMph: 30, maxSpeedMph: 40, estimatedCost: 2, estimatedCurrency: "USD",
                              estimatedPricePerKwh: nil, roundTripLegs: nil, drivingSeconds: nil)
        let ids = ActivityLogic.tiles(d, metric: false).map(\.id)
        XCTAssertTrue(ids.contains("energy"))
        XCTAssertFalse(ids.contains("cost"))
        XCTAssertFalse(ids.contains("efficiency"))
    }

    func testTheChargeTilesFollowTheLedgersSource() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let home = Dictionary(uniqueKeysWithValues: ActivityLogic.tiles(try XCTUnwrap(r.charges.first { $0.id == "chg-home-2" })).map { ($0.id, $0) })
        XCTAssertEqual(home["duration"]?.value, "5h 0m")
        XCTAssertEqual(home["soc"]?.value, "55% → 85%")
        XCTAssertEqual(home["energy"]?.value, "40.1")
        XCTAssertEqual(home["power"]?.value, "11.4")
        XCTAssertEqual(home["cost"]?.value, "$5.21")
        XCTAssertEqual(home["cost"]?.hint, "at your own rate")
        let away = Dictionary(uniqueKeysWithValues: ActivityLogic.tiles(try XCTUnwrap(r.charges.first { $0.id == "chg-away-1" })).map { ($0.id, $0) })
        XCTAssertEqual(away["cost"]?.value, "—")
        XCTAssertEqual(away["cost"]?.hint, "price unknown")
        let fast = ActivityLogic.tiles(try XCTUnwrap(r.charges.first { $0.id == "chg-fast-1" })).first { $0.id == "cost" }
        XCTAssertEqual(fast?.hint, "billed")
    }
}
