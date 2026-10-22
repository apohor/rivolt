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
            charges.append(ChargePoint(startedAt: t(day, from), endedAt: t(day, to), startSocPct: a, endSocPct: b, chargeLimitPct: limit, kind: kind))
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
        XCTAssertEqual(SocDaily.extremes(days)?.low.low, 3)
        XCTAssertEqual(SocDaily.extremes(days)?.high.high, 97)
        XCTAssertFalse(steps.isEmpty)
        try render("soc-daily", ThemeSection(title: "Battery (SoC) trend") {
            SocDailyChart(days: days, steps: steps, month: pm, now: now, calendar: utc)
                .padding(4 * Theme.unit)
        }.padding(16).frame(width: 390).background(ThemePageBackground()))
    }
}
