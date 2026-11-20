import XCTest
@testable import Rivolt

/// The Now card's parked graph numbers and words (design_preview key now_parked_graph), as the web's parkedSeries.ts and ParkedGraph: the same fixture
/// numbers (web/e2e/parkedSeriesFixture.ts), the header is the difference of the two labels, and the two idle-loss texts.
@MainActor
final class NowParkedGraphModelTests: XCTestCase {
    private func series(_ name: String) throws -> ParkedSeries { try Fixture.decode(ParkedSeries.self, "parked_series_\(name)") }

    private func model(_ name: String, nowPct: Double? = nil, metric: Bool = false) throws -> NowParkedGraphModel {
        try XCTUnwrap(NowParkedGraphModel.make(series: series(name), nowPct: nowPct, metric: metric))
    }

    func testTheHeaderIsTheEndLabelMinusTheStartLabel() throws {
        for name in ["overnight", "charged", "asleep", "never_slept"] {
            let m = try model(name)
            let delta = m.last.pct - m.start.pct
            XCTAssertEqual(Double(m.change.replacingOccurrences(of: "−", with: "-").replacingOccurrences(of: "%", with: "")) ?? .nan, (delta * 10).rounded() / 10, accuracy: 0.001, name)
        }
    }

    func testAnOvernightParkMeasuresFromTheParkedMarker() throws {
        let m = try model("overnight")
        let s = try series("overnight")
        // The drive 12 h ago is before the marker: the start label is the first reading of the current run, not of the window.
        XCTAssertEqual(m.start.pct, 77.4)
        XCTAssertEqual(m.last.pct, 75.7)
        XCTAssertEqual(m.change, "−1.7%")
        XCTAssertEqual(m.since, "since \(s.parkedSince!.formatted(date: .omitted, time: .shortened))")
        XCTAssertEqual(m.sleeps.count, 5)
        XCTAssertEqual(m.wakes.count, 4)
        XCTAssertNotNil(m.parkedX)
        XCTAssertEqual(m.parkedLabel, "Parked \(s.parkedSince!.formatted(date: .omitted, time: .shortened))")
    }

    func testAChargeBeforeTheMarkerNeverCounts() throws {
        let m = try model("charged")
        XCTAssertEqual(m.start.pct, 95.4)
        XCTAssertEqual(m.last.pct, 94.3)
        XCTAssertEqual(m.change, "−1.1%")
    }

    func testTheLineEndsAtTheRingsBattery() throws {
        let m = try model("overnight", nowPct: 75.0)
        XCTAssertEqual(m.last.pct, 75.0)
        XCTAssertEqual(m.change, "−2.4%")
        XCTAssertEqual(m.line.last?.x ?? 0, 1, accuracy: 0.0001)
    }

    func testParkedTheWholeWindowSaysIn24h() {
        let r = NowParkedGraphModel.parkedChange(base: 80, now: 78.6, since: nil)
        XCTAssertEqual(r.value, "−1.4%")
        XCTAssertEqual(r.since, "in 24 h")
        XCTAssertEqual(NowParkedGraphModel.parkedChange(base: 70, now: 70, since: nil).value, "0.0%")
        XCTAssertEqual(NowParkedGraphModel.parkedChange(base: 70, now: 70.4, since: nil).value, "+0.4%")
    }

    func testTheFixtureNumbersMatchTheWeb() throws {
        let f = try model("overnight").figures
        XCTAssertEqual(f.map(\.id.rawValue), ["asleep", "wakes", "idle"])
        XCTAssertEqual(f[0].value, "9 h 06 min")
        XCTAssertEqual(f[1].value, "4 wake-ups")
        XCTAssertEqual(f[1].tileNote, "≈ 0.4% each")
        XCTAssertEqual(f[2].value, "6% a day")
        XCTAssertEqual(f[2].label, "idle loss")
    }

    func testTheTwoIdleLossTexts() throws {
        let imperial = try model("overnight", metric: false).figures[2]
        XCTAssertEqual(imperial.note, "≈ 18 mi of range")
        XCTAssertEqual(imperial.tileNote, "≈ 18 mi")
        let metric = try model("overnight", metric: true).figures[2]
        XCTAssertEqual(metric.note, "≈ 29 km of range")
        XCTAssertEqual(metric.tileNote, "≈ 29 km")
    }

    func testNoWakeUpsMeansNoWakeTile() throws {
        XCTAssertEqual(try model("never_slept").figures.map(\.id.rawValue), ["asleep", "idle"])
        XCTAssertEqual(try model("never_slept").figures[1].note, "≈ 36 mi of range")
    }

    func testDurations() {
        XCTAssertEqual(NowParkedGraphModel.longDuration(45 * 60), "45 min")
        XCTAssertEqual(NowParkedGraphModel.longDuration(18 * 3600 + 40 * 60), "18 h 40 min")
        XCTAssertEqual(NowParkedGraphModel.bandDuration(6 * 3600 + 10 * 60), "6 h 10")
        XCTAssertEqual(NowParkedGraphModel.bandDuration(2 * 60), "2 min")
    }

    func testSleepsAreBandsInPlotSpaceAndNeverOverlap() throws {
        let m = try model("overnight")
        for (a, b) in zip(m.sleeps, m.sleeps.dropFirst()) { XCTAssertLessThanOrEqual(a.x1, b.x0) }
        XCTAssertTrue(m.sleeps.allSatisfy { $0.x0 >= 0 && $0.x1 <= 1 && $0.label.hasPrefix("☾ ") })
    }

    func testTheProjectionLine() throws {
        let p = try XCTUnwrap(try model("overnight").projection)
        XCTAssertTrue(p.hasPrefix("At this rate: about 64% by "), p)
        XCTAssertTrue(p.hasSuffix("(estimate)."), p)
    }

    func testTooLittleToDrawIsNil() throws {
        var s = try series("overnight")
        XCTAssertTrue(NowParkedGraphModel.drawable(s))
        XCTAssertFalse(NowParkedGraphModel.drawable(nil))
        s = ParkedSeries(version: 1, from: s.from, to: s.to, parkedSince: s.to, parked: true, changePct: nil, points: Array(s.points.prefix(3)), sleeps: [], wakes: [], figures: nil, projection: nil)
        XCTAssertNil(NowParkedGraphModel.make(series: s, nowPct: nil, metric: false))
    }

    func testTheSpokenLabelSaysTheSameNumbers() throws {
        let m = try model("overnight")
        XCTAssertTrue(m.spoken.contains("77 percent at"), m.spoken)
        XCTAssertTrue(m.spoken.contains("to 76 percent now, minus 1.7%"), m.spoken)
        XCTAssertTrue(m.spoken.hasSuffix("5 sleeps, 4 wake-ups."), m.spoken)
    }
}
