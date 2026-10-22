import SwiftUI
import XCTest
@testable import Rivolt

final class SocDailyTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private func at(_ s: String) -> Date { JSON.parseDate(s)! }

    private func drive(_ s: String, _ e: String, _ a: Double, _ b: Double) -> DrivePoint {
        DrivePoint(startedAt: at(s), endedAt: at(e), distanceMi: 10, startSocPct: a, endSocPct: b)
    }

    private func charge(_ s: String, _ e: String, _ a: Double, _ b: Double, kind: String? = "ac") -> ChargePoint {
        ChargePoint(startedAt: at(s), endedAt: at(e), startSocPct: a, endSocPct: b, kind: kind)
    }

    func testADayWithFourCyclesIsOneBarFromItsLowestToItsHighestAndEndsWhereItEnded() {
        // The owner's Oct 12: 58 → 22 → 73 → 3 → 73 → 17 → 42 → 12 → 70.
        let drives = [
            drive("2026-10-12T05:00:00Z", "2026-10-12T07:30:00Z", 58, 22),
            drive("2026-10-12T09:00:00Z", "2026-10-12T13:00:00Z", 73, 3),
            drive("2026-10-12T15:00:00Z", "2026-10-12T18:00:00Z", 73, 17),
            drive("2026-10-12T21:45:00Z", "2026-10-12T22:45:00Z", 42, 12),
        ]
        let charges = [
            charge("2026-10-12T07:45:00Z", "2026-10-12T08:35:00Z", 22, 73, kind: "dcfc"),
            charge("2026-10-12T13:30:00Z", "2026-10-12T14:30:00Z", 3, 73, kind: "dcfc"),
            charge("2026-10-12T18:30:00Z", "2026-10-12T21:30:00Z", 17, 42),
            charge("2026-10-12T22:50:00Z", "2026-10-12T23:50:00Z", 12, 70, kind: "dcfc"),
        ]
        let days = SocDaily.days(drives: drives, charges: charges, calendar: utc)
        XCTAssertEqual(days.count, 1)
        XCTAssertEqual(days[0].low, 3)
        XCTAssertEqual(days[0].high, 73)
        XCTAssertEqual(days[0].end, 70)
        XCTAssertTrue(days[0].charged)
        XCTAssertTrue(days[0].fast)
    }

    func testDaysWithoutReadingsAreSkippedAndAChargeFlagsTheDaysItTouches() {
        let drives = [drive("2026-10-02T13:00:00Z", "2026-10-02T13:30:00Z", 71, 69), drive("2026-10-05T13:00:00Z", "2026-10-05T13:30:00Z", 66, 62)]
        // An overnight charge: starts on the 2nd, ends on the 3rd.
        let charges = [charge("2026-10-02T22:00:00Z", "2026-10-03T02:00:00Z", 60, 70)]
        let days = SocDaily.days(drives: drives, charges: charges, calendar: utc)
        XCTAssertEqual(days.map(\.day), [at("2026-10-02T00:00:00Z"), at("2026-10-03T00:00:00Z"), at("2026-10-05T00:00:00Z")], "the 4th has no readings")
        XCTAssertEqual(days.map(\.charged), [true, true, false])
        XCTAssertEqual(days.map(\.fast), [false, false, false])
        XCTAssertEqual(days[0].end, 60, "the last reading of the 2nd is the charge's start")
        XCTAssertEqual(days[1].end, 70)
        XCTAssertEqual(days[1].low, 70)
    }

    func testTheDaysAreTheLocalDaysNotUTCs() {
        var chicago = Calendar(identifier: .gregorian)
        chicago.timeZone = TimeZone(identifier: "America/Chicago")!
        // 01:00Z on the 3rd is 20:00 on the 2nd in Chicago.
        let days = SocDaily.days(drives: [drive("2026-10-03T01:00:00Z", "2026-10-03T01:30:00Z", 70, 66)], charges: [], calendar: chicago)
        XCTAssertEqual(days.count, 1)
        XCTAssertEqual(days[0].day, chicago.startOfDay(for: at("2026-10-02T12:00:00Z")))
    }

    func testTheMonthsHighAndLowAreNamedWithTheirDays() {
        let drives = [
            drive("2026-10-09T07:00:00Z", "2026-10-09T14:00:00Z", 97, 41),
            drive("2026-10-12T09:00:00Z", "2026-10-12T13:00:00Z", 73, 3),
            drive("2026-10-15T09:00:00Z", "2026-10-15T09:30:00Z", 70, 66),
        ]
        let days = SocDaily.days(drives: drives, charges: [], calendar: utc)
        let e = SocDaily.extremes(days)
        XCTAssertEqual(e?.high.high, 97)
        XCTAssertEqual(SocDaily.dayLabel(e!.high.day, calendar: utc), "Oct 9")
        XCTAssertEqual(e?.low.low, 3)
        XCTAssertEqual(SocDaily.dayLabel(e!.low.day, calendar: utc), "Oct 12")
        XCTAssertNil(SocDaily.extremes([]))
        XCTAssertEqual(SocDaily.spoken(days, calendar: utc),
                       "3 days with readings. Lowest 3 percent on Oct 12; highest 97 percent on Oct 9.")
        XCTAssertEqual(SocDaily.spoken([], calendar: utc), "No readings this month.")
    }

    func testAReadingOfZeroIsUnknownNotALow() {
        let days = SocDaily.days(drives: [drive("2026-10-09T07:00:00Z", "2026-10-09T08:00:00Z", 80, 0)], charges: [], calendar: utc)
        XCTAssertEqual(days[0].low, 80, "0 is the recorder's 'unknown'")
    }
}
