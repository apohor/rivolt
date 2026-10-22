import XCTest
@testable import Rivolt

private func cal(_ zone: String) -> Calendar {
    var c = Calendar(identifier: .gregorian)
    c.timeZone = TimeZone(identifier: zone)!
    return c
}

private func at(_ s: String) -> Date { JSON.parseDate(s)! }

final class KpiLogicTests: XCTestCase {
    private func activity() throws -> ActivityResponse { try Fixture.decode(ActivityResponse.self, "activity") }

    func testTheDriveAndChargeStatsAreTheWebs() throws {
        let a = try activity()
        let d = KpiLogic.drives(a.drives)
        XCTAssertEqual(d.count, 3)
        XCTAssertEqual(d.miles, 18.6 + 61.4 + 42.3, accuracy: 1e-9)
        XCTAssertEqual(d.miPerKwh, d.milesForEnergy / d.energyUsedKwh, accuracy: 1e-9)
        XCTAssertEqual(d.maxSpeedMph, 72)
        let c = KpiLogic.charges(a.charges)
        XCTAssertEqual(c.count, 4)
        XCTAssertEqual(c.energyKwh, 16.2 + 40.1 + 71.3 + 12.5, accuracy: 1e-9)
        XCTAssertEqual(c.maxPowerKw, 198)
        XCTAssertEqual(c.cost, 5.21 + 23.41, accuracy: 1e-9, "billed or priced charges only; the unpriced ones add nothing")
        XCTAssertEqual(c.currency, "USD")
    }

    func testTheTilesReadAsOnTheWeb() throws {
        let a = try activity()
        let ledger = try Fixture.decode(LedgerMonthSpend.self, "ledger_monthly")
        let tiles = KpiLogic.tiles(drives: KpiLogic.drives(a.drives), charges: KpiLogic.charges(a.charges), since: nil, sinceKnown: true,
                                   ledger: ledger, ledgerPending: false, metric: false, calendar: cal("UTC"))
        let byID = Dictionary(uniqueKeysWithValues: tiles.map { ($0.id, $0) })
        XCTAssertEqual(byID["since"]?.value, "—")
        XCTAssertEqual(byID["since"]?.hint, "no finished charges yet")
        XCTAssertEqual(byID["miles"]?.value, "122.3")
        XCTAssertEqual(byID["miles"]?.unit, "mi")
        XCTAssertEqual(byID["miles"]?.hint, "3 drives · avg 40.8 mi")
        XCTAssertEqual(byID["energy"]?.value, "140.1")
        XCTAssertEqual(byID["energy"]?.hint, "4 charges · peak 198 kW")
        XCTAssertEqual(byID["efficiency"]?.unit, "mi/kWh")
        XCTAssertTrue(byID["efficiency"]?.hint?.hasPrefix("pack-side · 122 mi / 47 kWh") == true, byID["efficiency"]?.hint ?? "")
        XCTAssertEqual(byID["cost"]?.value, "$35.43", "the ledger's month, the number Money shows")
        XCTAssertEqual(byID["cost"]?.hint, "0.290 USD/mi · 4 sessions")
    }

    func testTheCostFallsBackToTheMonthsChargesWhenTheLedgerCantSayAndWaitsWhilePending() throws {
        let a = try activity()
        let d = KpiLogic.drives(a.drives), c = KpiLogic.charges(a.charges)
        func cost(_ ledger: LedgerMonthSpend?, pending: Bool) -> KpiTile? {
            KpiLogic.tiles(drives: d, charges: c, since: nil, sinceKnown: true, ledger: ledger, ledgerPending: pending, metric: false).first { $0.id == "cost" }
        }
        XCTAssertEqual(cost(nil, pending: true)?.value, "—")
        XCTAssertEqual(cost(nil, pending: false)?.value, "$28.62")
        let split = LedgerMonthSpend(currency: "USD", spend: .init(total: 99, billedTotal: 20, estimatedTotal: 5))
        XCTAssertEqual(split.statementTotal, 25, "billed plus estimated when the server gives both")
        let none = KpiLogic.tiles(drives: KpiLogic.drives([]), charges: KpiLogic.charges([]), since: nil, sinceKnown: true,
                                  ledger: LedgerMonthSpend(currency: "USD", spend: .init(total: 0, billedTotal: nil, estimatedTotal: nil)), ledgerPending: false, metric: false)
        XCTAssertEqual(none.first { $0.id == "cost" }?.hint, "no priced charges yet")
        XCTAssertEqual(none.first { $0.id == "miles" }?.value, "—")
    }

    func testEfficiencyFallsBackFromThePackToTheWallToMilesPerPercent() {
        func eff(_ ds: [ActivityDrive], _ cs: [ActivityCharge]) -> KpiTile? {
            KpiLogic.tiles(drives: KpiLogic.drives(ds), charges: KpiLogic.charges(cs), since: nil, sinceKnown: true, ledger: nil, ledgerPending: false, metric: false)
                .first { $0.id == "efficiency" }
        }
        func drive(energy: Double) -> ActivityDrive {
            ActivityDrive(id: "d", vehicleId: "v", startedAt: .now, endedAt: .now, distanceMi: 50, energyUsedKwh: energy, startSocPct: 80, endSocPct: 70,
                          avgSpeedMph: 40, maxSpeedMph: 66, estimatedCost: nil, estimatedCurrency: nil, roundTripLegs: nil, drivingSeconds: nil)
        }
        func charge(kwh: Double) -> ActivityCharge {
            ActivityCharge(id: "c", vehicleId: "v", startedAt: .now, endedAt: .now, energyAddedKwh: kwh, startSocPct: 40, endSocPct: 60, maxPowerKw: 11,
                           cost: 0, currency: "USD", costSourceEffective: nil, costSourceDerived: nil, estimatedCost: nil, estimatedCurrency: nil,
                           kind: nil, place: nil, network: nil)
        }
        XCTAssertEqual(eff([drive(energy: 20)], [charge(kwh: 25)])?.hint, "pack-side · 50 mi / 20 kWh")
        let wall = eff([drive(energy: 0)], [charge(kwh: 25)])
        XCTAssertEqual(wall?.value, "2.00")
        XCTAssertEqual(wall?.hint, "wall-to-wheel · 50 mi / 25 kWh")
        let pct = eff([drive(energy: 0)], [])
        XCTAssertEqual(pct?.unit, "mi/%")
        XCTAssertEqual(pct?.hint, "top speed 66 mph")
    }

    func testAMetricPhoneGetsKilometres() throws {
        let a = try activity()
        let tiles = KpiLogic.tiles(drives: KpiLogic.drives(a.drives), charges: KpiLogic.charges(a.charges), since: nil, sinceKnown: true,
                                   ledger: nil, ledgerPending: false, metric: true)
        let miles = try XCTUnwrap(tiles.first { $0.id == "miles" })
        XCTAssertEqual(miles.label, "Kilometres")
        XCTAssertEqual(miles.unit, "km")
        XCTAssertEqual(try XCTUnwrap(Double(miles.value.replacingOccurrences(of: ",", with: ""))), 122.3 * 1.609344, accuracy: 0.06)
        XCTAssertEqual(tiles.first { $0.id == "efficiency" }?.unit, "km/kWh")
    }

    func testSinceTheLastChargeSkipsTheOneInProgressAndCountsTheDrivesAfter() throws {
        func row(_ id: String, _ start: String, _ end: String?) throws -> ChargeBrief {
            let e = end.map { ",\"EndedAt\":\"\($0)\"" } ?? ""
            let json = "[{\"ID\":\"\(id)\",\"StartedAt\":\"\(start)\"\(e),\"EnergyAddedKWh\":1,\"Cost\":0}]"
            return try XCTUnwrap(JSON.decoder().decode([ChargeBrief].self, from: Data(json.utf8)).first)
        }
        let active = try row("now", "2026-10-21T20:00:00Z", nil)
        let done = try row("prev", "2026-10-19T00:00:00Z", "2026-10-19T04:00:00Z")
        // The in-progress charge (it began with the current session) doesn't count; the one before it does.
        XCTAssertEqual(KpiLogic.lastFinishedChargeEnd([active, done], activeStart: at("2026-10-21T20:02:00Z")), at("2026-10-19T04:00:00Z"))
        // With no session in progress, the newest row with an end time is the answer.
        XCTAssertEqual(KpiLogic.lastFinishedChargeEnd([done], activeStart: nil), at("2026-10-19T04:00:00Z"))
        XCTAssertNil(KpiLogic.lastFinishedChargeEnd([active], activeStart: at("2026-10-21T20:02:00Z")))
        XCTAssertNil(KpiLogic.lastFinishedChargeEnd([], activeStart: nil))
        let points = try Fixture.decode(ChartPoints.self, "chart_points")
        let since = KpiLogic.sinceCharge(end: at("2026-10-19T04:00:00Z"), points: points)
        XCTAssertEqual(since.drives, 1)
        XCTAssertEqual(since.miles, 9.8, accuracy: 1e-9)
        let tile = KpiLogic.tiles(drives: KpiLogic.drives([]), charges: KpiLogic.charges([]), since: since, sinceKnown: false,
                                  ledger: nil, ledgerPending: true, metric: false, calendar: cal("UTC")).first { $0.id == "since" }
        XCTAssertEqual(tile?.value, "9.8")
        XCTAssertEqual(tile?.hint, "1 drive · since Oct 19")
    }

    func testTheIdleTileHasTodaysFigureTheAverageOfFullDaysAndSevenBars() throws {
        let sleep = try Fixture.decode([DayActivity].self, "sleep_activity")
        let utc = cal("UTC")
        let t = try XCTUnwrap(IdleAwakeTile.make(sleep: sleep, now: at("2026-10-21T15:00:00Z"), calendar: utc))
        XCTAssertEqual(t.bars.count, 7)
        XCTAssertEqual(try XCTUnwrap(t.today), sleep.last!.idleAwakeH * 60, accuracy: 1e-9)
        let full = sleep.dropLast().map { $0.idleAwakeH * 60 }
        XCTAssertEqual(try XCTUnwrap(t.average), full.reduce(0, +) / Double(full.count), accuracy: 1e-9)
        XCTAssertEqual(t.averageDays, 6)
        // A day later, nothing is "today" yet.
        XCTAssertNil(IdleAwakeTile.make(sleep: sleep, now: at("2026-10-22T15:00:00Z"), calendar: utc)?.today)
        XCTAssertNil(IdleAwakeTile.make(sleep: nil, now: .now, calendar: utc))
    }

    func testTheSpokenTileNamesLabelValueAndHint() {
        let t = KpiTile(id: "miles", label: "Miles", value: "122.3", unit: "mi", hint: "3 drives · avg 40.8 mi")
        XCTAssertEqual(t.spoken, "Miles, 122.3 mi. 3 drives · avg 40.8 mi")
    }
}

private actor Calls {
    var activity = 0, ledger = 0, points = 0
    func a() { activity += 1 }
    func l() { ledger += 1 }
    func p() { points += 1 }
}

private struct FakeKpi: KpiSource {
    let calls = Calls()
    var activityResult: Result<ActivityResponse, Error>
    var ledgerResult: Result<LedgerMonthSpend, Error>
    func activity(since: Date, until: Date, vehicleID: String) async throws -> ActivityResponse {
        await calls.a()
        return try activityResult.get()
    }
    func ledgerSpend(month: String, timeZone: String, vehicleID: String?) async throws -> LedgerMonthSpend {
        await calls.l()
        return try ledgerResult.get()
    }
    func chartPoints(since: Date, until: Date, vehicleID: String) async throws -> ChartPoints {
        await calls.p()
        return ChartPoints(drives: [], charges: [])
    }
}

@MainActor
final class KpiModelTests: XCTestCase {
    private let utc = cal("UTC")
    private var m1: PeriodMonth { PeriodMonth(containing: at("2026-10-15T12:00:00Z"), calendar: utc) }
    private var m2: PeriodMonth { PeriodMonth(containing: at("2026-09-15T12:00:00Z"), calendar: utc) }
    private func ok() throws -> FakeKpi {
        FakeKpi(activityResult: .success(try Fixture.decode(ActivityResponse.self, "activity")),
                ledgerResult: .success(try Fixture.decode(LedgerMonthSpend.self, "ledger_monthly")))
    }

    func testAnAnswerIsReusedForFifteenMinutesAndAPullReadsAgain() async throws {
        let src = try ok()
        let model = KpiModel()
        let t0 = at("2026-10-21T15:00:00Z")
        await model.load(source: src, vehicleID: "a", month: m1, scopeLedger: false, lastChargeEnd: nil, now: t0, calendar: utc)
        await model.load(source: src, vehicleID: "a", month: m1, scopeLedger: false, lastChargeEnd: nil, now: t0.addingTimeInterval(14 * 60), calendar: utc)
        var n = await src.calls.activity
        XCTAssertEqual(n, 1)
        XCTAssertEqual(model.activity?.drives.count, 3)
        XCTAssertEqual(model.ledger?.statementTotal, 35.43)
        await model.load(source: src, vehicleID: "a", month: m2, scopeLedger: false, lastChargeEnd: nil, now: t0, calendar: utc)
        n = await src.calls.activity
        XCTAssertEqual(n, 2)
        await model.load(source: src, vehicleID: "a", month: m1, scopeLedger: false, lastChargeEnd: nil, force: true, now: t0.addingTimeInterval(14 * 60), calendar: utc)
        n = await src.calls.activity
        XCTAssertEqual(n, 3)
    }

    func testTheDrivesSinceTheLastChargeAreReadOnlyWhenThereIsAFinishedCharge() async throws {
        let src = try ok()
        let model = KpiModel()
        await model.load(source: src, vehicleID: "a", month: m1, scopeLedger: false, lastChargeEnd: nil, now: at("2026-10-21T15:00:00Z"), calendar: utc)
        var p = await src.calls.points
        XCTAssertEqual(p, 0)
        XCTAssertNil(model.since)
        await model.load(source: src, vehicleID: "a", month: m1, scopeLedger: false, lastChargeEnd: at("2026-10-19T04:00:00Z"), now: at("2026-10-21T15:00:00Z"), calendar: utc)
        p = await src.calls.points
        XCTAssertEqual(p, 1)
        XCTAssertEqual(model.since?.drives, 0)
    }

    func testARefusedReadKeepsWhatIsOnScreenAndAnotherMonthNeverShowsIt() async throws {
        let model = KpiModel()
        let t0 = at("2026-10-21T15:00:00Z")
        await model.load(source: try ok(), vehicleID: "a", month: m1, scopeLedger: false, lastChargeEnd: nil, now: t0, calendar: utc)
        let limited = FakeKpi(activityResult: .failure(APIError.server(status: 429, message: "slow down")),
                              ledgerResult: .failure(APIError.server(status: 429, message: "slow down")))
        await model.load(source: limited, vehicleID: "a", month: m1, scopeLedger: false, lastChargeEnd: nil, force: true, now: t0, calendar: utc)
        XCTAssertEqual(model.activity?.charges.count, 4)
        XCTAssertEqual(model.ledger?.statementTotal, 35.43)
        await model.load(source: limited, vehicleID: "a", month: m2, scopeLedger: false, lastChargeEnd: nil, now: t0, calendar: utc)
        XCTAssertNil(model.activity)
        XCTAssertNil(model.ledger)
    }

    func testAMissingLedgerLeavesOnlyTheCostToTheFallback() async throws {
        let src = FakeKpi(activityResult: .success(try Fixture.decode(ActivityResponse.self, "activity")), ledgerResult: .failure(APIError.transport("down")))
        let model = KpiModel()
        await model.load(source: src, vehicleID: "a", month: m1, scopeLedger: false, lastChargeEnd: nil, now: at("2026-10-21T15:00:00Z"), calendar: utc)
        XCTAssertNotNil(model.activity)
        XCTAssertNil(model.ledger)
        XCTAssertFalse(model.ledgerPending, "a failed ledger isn't waited for")
    }
}
