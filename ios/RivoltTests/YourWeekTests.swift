import XCTest
@testable import Rivolt

final class YourWeekTests: XCTestCase {
    private var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }
    /// Wednesday 28 Oct 2026, noon UTC. This week starts Monday 26 Oct; last week Monday 19 Oct.
    private let now = JSON.parseDate("2026-10-28T12:00:00Z")!
    private func at(_ day: Int, _ hour: Int = 9) -> Date { JSON.parseDate(String(format: "2026-10-%02dT%02d:00:00Z", day, hour))! }

    private func drive(_ day: Int, _ miles: Double, _ hour: Int = 9) -> ActivityDrive {
        ActivityDrive(id: "d\(day)\(hour)", vehicleId: "v", startedAt: at(day, hour), endedAt: at(day, hour + 1), distanceMi: miles, energyUsedKwh: 0,
                      startSocPct: 80, endSocPct: 70, avgSpeedMph: 40, maxSpeedMph: 60, estimatedCost: nil, estimatedCurrency: nil,
                      estimatedPricePerKwh: nil, roundTripLegs: nil, drivingSeconds: nil)
    }
    private func charge(_ day: Int, _ kwh: Double, cost: Double = 0, source: String? = nil, estimated: Double? = nil) -> ActivityCharge {
        ActivityCharge(id: "c\(day)", vehicleId: "v", startedAt: at(day, 18), endedAt: at(day, 20), energyAddedKwh: kwh, startSocPct: 40, endSocPct: 60,
                       maxPowerKw: 7, cost: cost, currency: "USD", costSourceEffective: source, costSourceDerived: nil, estimatedCost: estimated,
                       estimatedCurrency: "USD", kind: "ac", place: "home", network: nil)
    }
    private func week(_ ds: [ActivityDrive], _ cs: [ActivityCharge]) -> YourWeekLogic.Week? {
        YourWeekLogic.week(drives: ds, charges: cs, now: now, calendar: cal)
    }

    func testTheWeekStartsOnMondayWhateverTheCalendarsFirstWeekday() {
        var sunday = cal
        sunday.firstWeekday = 1
        XCTAssertEqual(YourWeekLogic.weekStart(now, calendar: sunday), JSON.parseDate("2026-10-26T00:00:00Z")!)
        XCTAssertEqual(YourWeekLogic.weekStart(JSON.parseDate("2026-11-01T23:00:00Z")!, calendar: cal), JSON.parseDate("2026-10-26T00:00:00Z")!, "Sunday is the last day")
    }

    func testNothingRecordedThisWeekIsNoStrip() {
        XCTAssertNil(week([], []))
        XCTAssertNil(week([drive(20, 50)], [charge(21, 20)]))
    }

    func testItSumsThisWeeksMilesEnergyAndCost() throws {
        let w = try XCTUnwrap(week([drive(26, 40), drive(27, 60), drive(25, 99)], [charge(27, 30, cost: 9, source: "rivian"), charge(20, 80)]))
        XCTAssertEqual(w.figures.miles, 100)
        XCTAssertEqual(w.figures.kWh, 30)
        XCTAssertEqual(w.figures.cost, 9)
        XCTAssertFalse(w.figures.costEstimated)
    }

    func testTheCostIsAnEstimateUnlessRivianOrTheOwnerStatedEveryPrice() throws {
        XCTAssertFalse(try XCTUnwrap(week([drive(26, 10)], [charge(26, 10, cost: 4, source: "user")])).figures.costEstimated)
        XCTAssertTrue(try XCTUnwrap(week([drive(26, 10)], [charge(26, 10, cost: 4, source: "home_rate")])).figures.costEstimated)
        let only = try XCTUnwrap(week([drive(26, 10)], [charge(26, 10, estimated: 3)]))
        XCTAssertTrue(only.figures.costEstimated)
        XCTAssertEqual(only.figures.cost, 3)
    }

    func testMilesAreComparedWithLastWeekAtTheSameMomentNotTheWholeWeek() throws {
        // 80 mi by Wednesday noon last week; the weekend's 400 mi is not in the comparison.
        XCTAssertEqual(try XCTUnwrap(week([drive(26, 60), drive(19, 50), drive(21, 30), drive(24, 400)], [])).milesChangePct, -25)
        // A drive after that moment last week (Wed 20:00) is left out too.
        XCTAssertEqual(try XCTUnwrap(week([drive(26, 60), drive(19, 50), drive(21, 30, 20)], [])).milesChangePct, 20)
    }

    func testNoPercentageWhenLastWeekGivesNothingToCompareWith() throws {
        XCTAssertNil(try XCTUnwrap(week([drive(26, 60)], [])).milesChangePct)
        XCTAssertNil(try XCTUnwrap(week([drive(26, 60), drive(19, 4)], [])).milesChangePct)
        XCTAssertNil(try XCTUnwrap(week([drive(19, 80)], [charge(27, 20)])).milesChangePct, "only charging this week")
    }

    func testTheChangeInPlainWords() {
        XCTAssertEqual(YourWeekLogic.changeLine(12), "▲ 12% miles vs last week so far")
        XCTAssertEqual(YourWeekLogic.changeLine(-30), "▼ 30% miles vs last week so far")
        XCTAssertEqual(YourWeekLogic.changeLine(0), "About the same miles as last week so far")
        XCTAssertNil(YourWeekLogic.changeLine(nil))
    }
}
