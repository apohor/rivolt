import XCTest
@testable import Rivolt

final class YearCardTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private func full() throws -> YearInRivolt { try Fixture.decode(YearInRivolt.self, "year_in_rivolt") }

    func testTheServersFixturesDecode() throws {
        let y = try full()
        XCTAssertTrue(y.eligible)
        XCTAssertEqual(y.model, "R1S")
        XCTAssertEqual(y.energyShares?.home, 0.62)
        XCTAssertEqual(y.mostEfficientLongDrive?.miPerKwh, 3.1)
        let no = try Fixture.decode(YearInRivolt.self, "year_in_rivolt_not_yet")
        XCTAssertFalse(no.eligible)
        XCTAssertEqual(no.reason, "not_enough_activity")
    }

    func testTheCardIsOfferedInJanuaryForTheYearJustEnded() {
        func d(_ s: String) -> Date { JSON.parseDate(s)! }
        XCTAssertEqual(YearCardLogic.year(now: d("2027-01-05T12:00:00Z"), calendar: utc), 2026)
        XCTAssertEqual(YearCardLogic.year(now: d("2027-01-31T12:00:00Z"), calendar: utc), 2026)
        XCTAssertNil(YearCardLogic.year(now: d("2026-10-30T12:00:00Z"), calendar: utc))
        XCTAssertNil(YearCardLogic.year(now: d("2026-12-31T12:00:00Z"), calendar: utc))
    }

    func testAnIneligibleAnswerDrawsNothing() throws {
        XCTAssertNil(YearCardLogic.card(try Fixture.decode(YearInRivolt.self, "year_in_rivolt_not_yet"), includeCost: false))
    }

    func testTheCardHasNoMoneyByDefaultAndSaysWhatTheFiguresAre() throws {
        let c = try XCTUnwrap(YearCardLogic.card(try full(), includeCost: false))
        XCTAssertEqual(c.title, "2026 in your R1S")
        XCTAssertEqual(c.eyebrow, "Recorded by Rivolt · since March")
        XCTAssertEqual(c.subtitle, "612 drives · 96 charges · 19 stops on 7 planned trips")
        XCTAssertLessThanOrEqual(c.stats.count, 6)
        let rows = Dictionary(uniqueKeysWithValues: c.stats.map { ($0.label, $0.value) })
        XCTAssertEqual(rows["kWh used driving"], "3,012 kWh")
        XCTAssertEqual(rows["Efficiency"], "about 2.8 mi/kWh")
        XCTAssertEqual(rows["Longest trip"], "413 mi · July")
        XCTAssertEqual(rows["Charged"], "62% home · 11% away · 27% fast")
        XCTAssertEqual(rows["Coldest drive"], "4°F · January")
        let text = ([c.eyebrow, c.title, c.subtitle] + c.stats.map { $0.label + $0.value } + c.notes).joined()
        XCTAssertFalse(text.contains("$"))
        XCTAssertFalse(text.lowercased().contains("cost"))
        XCTAssertTrue(c.compact)
    }

    func testCostIsAnEstimateAndOnlyWhenAsked() throws {
        let c = try XCTUnwrap(YearCardLogic.card(try full(), includeCost: true))
        XCTAssertEqual(c.stats.first { $0.label == "Cost (estimate)" }?.value, "~$611 · ~$0.07/mi")
        XCTAssertLessThanOrEqual(c.stats.count, 6)
    }

    func testLinesWithNothingTrueToSayAreHidden() throws {
        let json = """
        {"eligible": true, "year": 2026, "drive_count": 12, "miles": 300, "kwh": 100, "charge_count": 4, "planned_trips": 0, "planned_stops": 0}
        """
        let y = try JSON.decoder().decode(YearInRivolt.self, from: Data(json.utf8))
        let c = try XCTUnwrap(YearCardLogic.card(y, includeCost: true))
        XCTAssertEqual(c.title, "2026 in Rivolt")
        XCTAssertEqual(c.eyebrow, "Recorded by Rivolt")
        XCTAssertEqual(c.subtitle, "12 drives · 4 charges")
        XCTAssertEqual(c.stats.map(\.label), ["Driven", "kWh used driving"])
    }

    func testAMalformedMonthIsDropped() {
        XCTAssertNil(YearCardLogic.monthName("2026-13"))
        XCTAssertNil(YearCardLogic.monthName(nil))
        XCTAssertEqual(YearCardLogic.monthName("2026-07"), "July")
        XCTAssertEqual(YearCardLogic.fileName(2026), "rivolt-year-2026.png")
    }
}
