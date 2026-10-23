import SwiftUI
import XCTest
@testable import Rivolt

/// The Battery (SoC) trend on a month with a road trip: a big swing, sparse samples, a short drive.
final class SocChartTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private func date(_ s: String) throws -> Date { try XCTUnwrap(JSON.parseDate(s)) }

    /// October 2026 as the owner's truck had it: charged to 97% on the 9th, driven down to 2.6% by the 12th.
    private func roadTrip() throws -> ChartPoints {
        let json = """
        {"drives":[
          {"started_at":"2026-10-02T13:00:00Z","ended_at":"2026-10-02T13:20:00Z","distance_mi":9,"start_soc_pct":71,"end_soc_pct":69},
          {"started_at":"2026-10-05T22:00:00Z","ended_at":"2026-10-05T22:40:00Z","distance_mi":22,"start_soc_pct":69,"end_soc_pct":61},
          {"started_at":"2026-10-09T15:00:00Z","ended_at":"2026-10-09T23:30:00Z","distance_mi":286,"start_soc_pct":97,"end_soc_pct":41},
          {"started_at":"2026-10-11T14:00:00Z","ended_at":"2026-10-11T22:00:00Z","distance_mi":240,"start_soc_pct":70,"end_soc_pct":3},
          {"started_at":"2026-10-12T16:00:00Z","ended_at":"2026-10-12T16:25:00Z","distance_mi":12,"start_soc_pct":2.6,"end_soc_pct":2.6},
          {"started_at":"2026-10-18T14:00:00Z","ended_at":"2026-10-18T14:30:00Z","distance_mi":14,"start_soc_pct":88,"end_soc_pct":84},
          {"started_at":"2026-10-20T09:00:00Z","ended_at":"2026-10-20T09:18:00Z","distance_mi":10,"start_soc_pct":80,"end_soc_pct":77}
        ],"charges":[
          {"started_at":"2026-10-03T01:00:00Z","ended_at":"2026-10-03T07:00:00Z","start_soc_pct":66,"end_soc_pct":70,"charge_limit_pct":70},
          {"started_at":"2026-10-08T01:00:00Z","ended_at":"2026-10-08T09:00:00Z","start_soc_pct":58,"end_soc_pct":97,"charge_limit_pct":100},
          {"started_at":"2026-10-09T23:40:00Z","ended_at":"2026-10-10T01:10:00Z","start_soc_pct":41,"end_soc_pct":70,"charge_limit_pct":70},
          {"started_at":"2026-10-12T17:00:00Z","ended_at":"2026-10-13T06:00:00Z","start_soc_pct":2.6,"end_soc_pct":88,"charge_limit_pct":85},
          {"started_at":"2026-10-19T01:00:00Z","ended_at":"2026-10-19T05:00:00Z","start_soc_pct":84,"end_soc_pct":80,"charge_limit_pct":80}
        ]}
        """
        return try JSON.decoder().decode(ChartPoints.self, from: Data(json.utf8))
    }

    func testBothChartsSpanTheWholeMonthAndLabelTheFirstEleventhTwentyFirstAndLastDay() throws {
        func labels(_ iso: String) throws -> [String] {
            let m = PeriodMonth(containing: try date(iso), calendar: utc)
            return m.axisDays(calendar: utc).map { ChartsSection.dayLabel($0, calendar: utc) }
        }
        XCTAssertEqual(try labels("2026-10-22T10:00:00Z"), ["10-01", "10-11", "10-21", "10-31"])
        XCTAssertEqual(try labels("2026-11-03T10:00:00Z"), ["11-01", "11-11", "11-21", "11-30"])
        XCTAssertEqual(try labels("2026-02-10T10:00:00Z"), ["02-01", "02-11", "02-21", "02-28"])
        let m = PeriodMonth(containing: try date("2026-10-22T10:00:00Z"), calendar: utc)
        XCTAssertEqual(m.allDays(calendar: utc).count, 31, "the current month is drawn whole; the days after today stay empty")
        XCTAssertEqual(m.days(now: try date("2026-10-22T10:00:00Z"), calendar: utc).count, 22, "the data still stops at today")
    }

    func testTheLimitIsDrawnOnlyOnTheCurrentMonthAndOnlyForARealLimit() throws {
        let since = try date("2026-10-01T00:00:00Z"), until = try date("2026-11-01T00:00:00Z")
        let now = try date("2026-10-22T10:00:00Z")
        XCTAssertTrue(ChartMath.limitVisible(70, now: now, since: since, until: until))
        XCTAssertFalse(ChartMath.limitVisible(70, now: try date("2026-11-03T10:00:00Z"), since: since, until: until), "a past month")
        XCTAssertFalse(ChartMath.limitVisible(70, now: try date("2026-09-30T10:00:00Z"), since: since, until: until), "a future month")
        XCTAssertFalse(ChartMath.limitVisible(100, now: now, since: since, until: until), "100% is no limit worth a line")
        XCTAssertFalse(ChartMath.limitVisible(0, now: now, since: since, until: until))
        XCTAssertFalse(ChartMath.limitVisible(nil, now: now, since: since, until: until))
    }

    func testSeveralLimitsInAMonthNeverDrawOneLineAcrossIt() throws {
        let p = try roadTrip()   // limits 70, 100, 70, 85 and 80 over the month
        let since = try date("2026-10-01T00:00:00Z"), until = try date("2026-11-01T00:00:00Z")
        let now = try date("2026-10-22T10:00:00Z")
        let month = until.timeIntervalSince(since)
        let seg = try XCTUnwrap(ChartMath.limitSegment(p.charges, from: since, to: until, now: now, live: 70))
        XCTAssertEqual(seg.pct, 70, "today's target")
        XCTAssertEqual(seg.end, now, "to now: the days to come stay empty")
        XCTAssertGreaterThan(seg.start.timeIntervalSince(since), month / 2, "from the last change, not from the start of the month")
        XCTAssertLessThan(seg.end.timeIntervalSince(seg.start), month / 2)
        // An earlier month: the last limit known in it, to its end.
        let later = try date("2026-11-12T10:00:00Z")
        let past = try XCTUnwrap(ChartMath.limitSegment(p.charges, from: since, to: until, now: later, live: nil))
        XCTAssertEqual(past.pct, 80)
        XCTAssertEqual(past.end, until)
        XCTAssertLessThan(past.end.timeIntervalSince(past.start), month / 2)
        // No stored limits (an older server): today's target over the last two days, current month only.
        let bare = [ChargePoint(startedAt: try date("2026-10-03T01:00:00Z"), endedAt: try date("2026-10-03T07:00:00Z"), startSocPct: 66, endSocPct: 70)]
        let old = try XCTUnwrap(ChartMath.limitSegment(bare, from: since, to: until, now: now, live: 70))
        XCTAssertEqual(old.end.timeIntervalSince(old.start), 2 * 86_400)
        XCTAssertNil(ChartMath.limitSegment(bare, from: since, to: until, now: later, live: 70))
        XCTAssertNil(ChartMath.limitSegment([], from: since, to: until, now: now, live: nil))
    }

    func testTheHighAndLowAreTheRoadTripsSwing() throws {
        let p = try roadTrip()
        let e = try XCTUnwrap(SocDaily.extremes(SocDaily.days(drives: p.drives, charges: p.charges, calendar: utc)))
        XCTAssertEqual(e.high.high, 97)
        XCTAssertEqual(e.low.low, 2.6)
        XCTAssertEqual(NumberText.fixed(e.low.low, 0), "3")
    }

    @MainActor
    func testRenderTheRoadTripMonth() throws {
        Theme.registerFonts()
        // The 22nd: room to the right of now for the limit's value. The 29th: none, so it goes above the segment.
        for (name, nowISO) in [("soc-roadtrip", "2026-10-22T10:00:00Z"), ("soc-roadtrip-late", "2026-10-29T10:00:00Z")] {
            let now = try date(nowISO)
            let month = PeriodMonth(containing: now, calendar: utc)
            let charts = ChartsModel(points: try roadTrip(), sleep: [])
            let page = ChartsSection(model: charts, month: month, limit: 70, now: now, calendar: utc)
                .padding(16).frame(width: 390).background(ThemePageBackground())
            for scheme in [ColorScheme.light, .dark] {
                let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).fixedSize(horizontal: false, vertical: true))
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
    }
}
