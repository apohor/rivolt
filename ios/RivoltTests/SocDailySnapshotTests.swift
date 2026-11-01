import SwiftUI
import XCTest
@testable import Rivolt

/// The daily-range design on a month shaped like the owner's October: ordinary days between 52 and 70%, charged every third day or so, a charge to 97%
/// on the 8th, a drive down on the 9th, two quiet days, and the 12th with four cycles (58 → 22 → 73 → 3 → 73 → 17 → 42 →
/// 12 → 70) on fast chargers.
@MainActor
final class SocDailySnapshotTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private let oct1 = Date(timeIntervalSince1970: 1_790_812_800)

    private func t(_ day: Int, _ hour: Double) -> Date { oct1.addingTimeInterval(Double(day - 1) * 86_400 + hour * 3_600) }

    private func month() -> (drives: [DrivePoint], charges: [ChargePoint]) {
        var drives: [DrivePoint] = []
        var charges: [ChargePoint] = []
        var level = 66.0
        func drive(_ day: Int, _ from: Double, _ to: Double, _ a: Double, _ b: Double) {
            drives.append(DrivePoint(startedAt: t(day, from), endedAt: t(day, to), distanceMi: (a - b) * 2.4, startSocPct: a, endSocPct: b))
        }
        func charge(_ day: Int, _ from: Double, _ to: Double, _ a: Double, _ b: Double, limit: Double, kind: String) {
            charges.append(ChargePoint(startedAt: t(day, from), endedAt: t(day, to), startSocPct: a, endSocPct: b, chargeLimitPct: limit))
        }
        for day in 1...22 {
            switch day {
            case 8:
                drive(8, 9, 10, level, level - 3); level -= 3
                drive(8, 17, 18, level, level - 5); level -= 5
                charge(8, 20, 24, level, 97, limit: 100, kind: "ac"); level = 97
            case 9:
                drive(9, 7, 14, 97, 41)
                charge(9, 14.5, 15.4, 41, 70, limit: 70, kind: "dcfc")
                drive(9, 16, 20, 70, 58); level = 58
            case 10, 11:
                break
            case 12:
                drive(12, 5, 7.5, 58, 22)
                charge(12, 7.75, 8.6, 22, 73, limit: 85, kind: "dcfc")
                drive(12, 9, 13, 73, 3)
                charge(12, 13.5, 14.5, 3, 73, limit: 85, kind: "dcfc")
                drive(12, 15, 18, 73, 17)
                charge(12, 18.5, 21.5, 17, 42, limit: 85, kind: "ac")
                drive(12, 21.75, 22.75, 42, 12)
                charge(12, 22.8, 23.9, 12, 70, limit: 85, kind: "dcfc"); level = 70
            default:
                drive(day, 8, 8.5, level, level - 3); level -= 3
                drive(day, 18, 18.6, level, level - 3); level -= 3
                if level < 58 {
                    charge(day, 22, 23.9, level, 70, limit: day >= 19 ? 80 : 70, kind: "ac"); level = 70
                }
            }
        }
        return (drives, charges)
    }

    private func render(_ name: String, _ content: some View) throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: content.environment(\.colorScheme, scheme).fixedSize(horizontal: false, vertical: true))
            renderer.scale = 3
            let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
            let png = try XCTUnwrap(image.pngData())
            let file = "\(name)-\(scheme == .dark ? "dark" : "light").png"
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = file
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
            }
        }
    }

    func testRenderTheDailyRangeDesign() throws {
        let m = month()
        let days = SocDaily.days(drives: m.drives, charges: m.charges, calendar: utc)
        let now = t(22, 12)
        let pm = PeriodMonth(containing: now, calendar: utc)
        let r = pm.range(calendar: utc)
        let steps = ChartMath.limitSteps(m.charges, from: r.since, to: r.until, now: now, live: 70)
        let limit = ChartMath.limitSegment(m.charges, from: r.since, to: r.until, now: now, live: 70)
        XCTAssertEqual(SocDaily.extremes(days)?.low.low, 3)
        XCTAssertEqual(SocDaily.extremes(days)?.high.high, 97)
        XCTAssertFalse(steps.isEmpty)
        try render("soc-daily", ThemeSection(title: "Battery (SoC) trend") {
            SocDailyChart(days: days, limit: limit, month: pm, calendar: utc)
                .padding(4 * Theme.unit)
        }.padding(16).frame(width: 390).background(ThemePageBackground()))
    }

    /// The first of a month with one day of readings: a drive from 37 to 32% and a charge to a 35% limit, at the largest chart text size. The
    /// high, the low and the limit sit within a few points of each other; their names must not land on one another.
    func testRenderAOneDayMonthAtALargeTextSize() throws {
        let nov1 = Date(timeIntervalSince1970: 1_793_491_200)
        let drives = [DrivePoint(startedAt: nov1.addingTimeInterval(14 * 3_600), endedAt: nov1.addingTimeInterval(14.5 * 3_600), distanceMi: 12, startSocPct: 37, endSocPct: 32)]
        let charges = [ChargePoint(startedAt: nov1.addingTimeInterval(20 * 3_600), endedAt: nov1.addingTimeInterval(21 * 3_600), startSocPct: 32, endSocPct: 35, chargeLimitPct: 35)]
        let days = SocDaily.days(drives: drives, charges: charges, calendar: utc)
        let now = nov1.addingTimeInterval(22 * 3_600)
        let pm = PeriodMonth(containing: now, calendar: utc)
        let r = pm.range(calendar: utc)
        let limit = ChartMath.limitSegment(charges, from: r.since, to: r.until, now: now, live: 35)
        XCTAssertEqual(days.count, 1)
        try render("soc-one-day-large", ThemeSection(title: "Battery (SoC) trend") {
            SocDailyChart(days: days, limit: limit, month: pm, calendar: utc)
                .padding(4 * Theme.unit)
        }.padding(16).frame(width: 390).background(ThemePageBackground()).environment(\.dynamicTypeSize, .xxxLarge))
    }

    func testAHighAndLowOnOneDayShareOneName() {
        let d = SocDaily.Day(day: oct1, low: 32, high: 37, end: 35, charged: false)
        let e = (high: d, low: d)
        XCTAssertEqual(SocDaily.extremeNames(e, calendar: utc), .range("32–37% · Oct 1"))
        let later = SocDaily.Day(day: oct1.addingTimeInterval(3 * 86_400), low: 20, high: 30, end: 25, charged: false)
        XCTAssertEqual(SocDaily.extremeNames((high: d, low: later), calendar: utc), .separate(high: "37% · Oct 1", low: "20% · Oct 4"))
    }

    func testTheLimitNameMovesOffAnExtremeNameAtItsLevel() {
        XCTAssertEqual(SocDaily.limitName(limitPct: 80, tight: false, extremeLevels: [97, 3]), .afterEnd)
        XCTAssertEqual(SocDaily.limitName(limitPct: 80, tight: true, extremeLevels: [97, 3]), .aboveEnd)
        XCTAssertEqual(SocDaily.limitName(limitPct: 35, tight: true, extremeLevels: [37, 32]), .rightEdge)
        XCTAssertEqual(SocDaily.limitName(limitPct: 35, tight: false, extremeLevels: []), .afterEnd)
    }
}
