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
        XCTAssertEqual(NowParkedGraphModel.parkedChange(base: 70, now: 70, since: nil).value, "No loss")
        XCTAssertEqual(NowParkedGraphModel.parkedChange(base: 75.72, now: 75.7, since: nil).value, "No loss")
        XCTAssertEqual(NowParkedGraphModel.parkedChange(base: 70, now: 70.4, since: nil).value, "+0.4%")
    }

    func testTheFixtureNumbersMatchTheWeb() throws {
        let f = try model("overnight").figures
        XCTAssertEqual(f.map(\.id.rawValue), ["asleep", "wakes", "idle"])
        XCTAssertEqual(f[0].value, "9 h 06 min")
        XCTAssertEqual(f[1].value, "4 wake-ups")
        XCTAssertEqual(f[1].tileNote, "0.4% each")
        XCTAssertEqual(f[2].value, "6% a day")
        XCTAssertEqual(f[2].label, "idle loss")
    }

    /// The asleep fixture with the figures the server gives a truck that lost nothing: wake cost 0, no idle loss.
    private func flat() throws -> ParkedSeries {
        var j = try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data("parked_series_asleep")) as? [String: Any])
        var f = try XCTUnwrap(j["figures"] as? [String: Any])
        f["wake_cost_pct"] = 0.02
        f["idle_pct_per_day"] = NSNull()
        f["idle_mi_per_day"] = NSNull()
        j["figures"] = f
        return try JSON.decoder().decode(ParkedSeries.self, from: JSONSerialization.data(withJSONObject: j))
    }

    /// The asleep fixture as a sleeping truck sends it: the last stored reading is 90 minutes before the request ("to"), as an asleep truck stores nothing for hours.
    private func stale() throws -> ParkedSeries {
        var j = try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data("parked_series_asleep")) as? [String: Any])
        let to = try XCTUnwrap(JSON.decoder().decode(ParkedSeries.self, from: JSONSerialization.data(withJSONObject: j)).to)
        let cut = to.addingTimeInterval(-90 * 60)
        let fmt = ISO8601DateFormatter()
        fmt.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let pts = try XCTUnwrap(j["points"] as? [[String: Any]]).filter { fmt.date(from: $0["t"] as? String ?? "").map { $0 <= cut } ?? false }
        j["points"] = pts
        return try JSON.decoder().decode(ParkedSeries.self, from: JSONSerialization.data(withJSONObject: j))
    }

    func testTheAxisEndsAtNowWhenTheLastStoredReadingIsNinetyMinutesOld() throws {
        let s = try stale()
        let lastStored = try XCTUnwrap(s.points.last)
        XCTAssertEqual(s.to.timeIntervalSince(lastStored.t), 90 * 60, accuracy: 600)
        let m = try XCTUnwrap(NowParkedGraphModel.make(series: s, nowPct: 71.0, metric: false))
        // The line runs to the right edge at the ring's value, not to the last reading.
        XCTAssertEqual(m.last.pct, 71.0)
        XCTAssertEqual(m.last.pt.x, 1, accuracy: 0.0001)
        XCTAssertEqual(m.line.last?.x ?? 0, 1, accuracy: 0.0001)
        XCTAssertLessThan(m.line.dropLast().last?.x ?? 1, 0.99)
        // The open sleep extends to now, and the header measures from the parked marker to the ring's value.
        let open = try XCTUnwrap(m.sleeps.last)
        XCTAssertEqual(open.x1, 1, accuracy: 0.0001)
        XCTAssertEqual(m.change, NowParkedGraphModel.parkedChange(base: m.start.pct, now: 71.0, since: s.parkedSince).value)
        XCTAssertEqual(NowParkedGraphModel.openSleepSince(s), s.sleeps.last?.from)
    }

    func testAWakeCostThatRoundsToZeroIsNotPrintedAndTwoTilesRemain() throws {
        let m = try XCTUnwrap(NowParkedGraphModel.make(series: flat(), nowPct: nil, metric: false))
        XCTAssertEqual(m.figures.map(\.id.rawValue), ["asleep", "wakes"])
        XCTAssertEqual(m.figures[1].tileNote, "")
        XCTAssertEqual(m.figures[1].note, "")
        XCTAssertEqual(m.figures[1].value, "\(try series("asleep").figures!.wakeUps) wake-ups")
    }

    func testAnAsleepChipTakesTheStartOfTheOpenSleepFromTheSeries() async throws {
        let s = try series("asleep")
        let open = try XCTUnwrap(s.sleeps.last)
        XCTAssertTrue(open.open)
        XCTAssertEqual(NowParkedGraphModel.openSleepSince(s), .some(open.from))
        let m = NowParkedSeriesModel()
        let state = Date(timeIntervalSince1970: 1_000)
        XCTAssertNil(m.chipAsleepSince(vehicleID: "a", state: state, late: false))
        await m.load(source: Source(answer: s, fails: false), vehicleID: "a")
        XCTAssertEqual(m.chipAsleepSince(vehicleID: "a", state: state, late: false), open.from)
        // Another truck: nothing of this one's.
        XCTAssertNil(m.chipAsleepSince(vehicleID: "b", state: state, late: false))
        // No open sleep in the series (the overnight one ended): the state's own time.
        let o = try series("overnight")
        XCTAssertNil(NowParkedGraphModel.openSleepSince(o))
        let m2 = NowParkedSeriesModel()
        await m2.load(source: Source(answer: o, fails: false), vehicleID: "a")
        XCTAssertEqual(m2.chipAsleepSince(vehicleID: "a", state: state, late: false), state)
        // A failed read ends the wait with the state's time.
        let m3 = NowParkedSeriesModel()
        await m3.load(source: Source(answer: nil, fails: true), vehicleID: "a")
        XCTAssertEqual(m3.chipAsleepSince(vehicleID: "a", state: state, late: false), state)
    }

    func testTheTwoIdleLossTexts() throws {
        let imperial = try model("overnight", metric: false).figures[2]
        XCTAssertEqual(imperial.note, "18 mi of range")
        XCTAssertEqual(imperial.tileNote, "18 mi")
        let metric = try model("overnight", metric: true).figures[2]
        XCTAssertEqual(metric.note, "29 km of range")
        XCTAssertEqual(metric.tileNote, "29 km")
    }

    func testNoWakeUpsMeansNoWakeTile() throws {
        XCTAssertEqual(try model("never_slept").figures.map(\.id.rawValue), ["asleep", "idle"])
        XCTAssertEqual(try model("never_slept").figures[1].note, "36 mi of range")
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

    private struct Source: ParkedSeriesSource {
        let answer: ParkedSeries?
        let fails: Bool
        func parkedSeries(vehicleID: String, tz: String) async throws -> ParkedSeries? {
            if fails { throw URLError(.timedOut) }
            return answer
        }
    }

    func testTheChipWaitsForTheSeriesAnswerOrItsFailure() async throws {
        let m = NowParkedSeriesModel()
        XCTAssertFalse(m.settled)
        await m.load(source: Source(answer: try series("overnight"), fails: false), vehicleID: "v")
        XCTAssertTrue(m.settled)
        XCTAssertNotNil(m.series?.parkedSince)
        m.clear()
        XCTAssertFalse(m.settled)
        await m.load(source: Source(answer: nil, fails: true), vehicleID: "v")
        XCTAssertTrue(m.settled)
        XCTAssertNil(m.series)
    }

    func testAnotherTruckStartsTheWaitOver() async throws {
        let m = NowParkedSeriesModel()
        await m.load(source: Source(answer: try series("overnight"), fails: false), vehicleID: "a")
        XCTAssertEqual(m.vehicleID, "a")
        await m.load(source: Source(answer: nil, fails: true), vehicleID: "b")
        XCTAssertEqual(m.vehicleID, "b")
        XCTAssertNil(m.series)
        XCTAssertTrue(m.settled)
    }

    func testTheChipShowsNoTimeUntilTheSeriesAnswersAndAgainAfterAReturn() async throws {
        let m = NowParkedSeriesModel()
        let state = Date(timeIntervalSince1970: 1_000)
        XCTAssertNil(m.chipSince(vehicleID: "a", state: state, late: false))
        XCTAssertEqual(m.chipSince(vehicleID: "a", state: state, late: true), state)
        let s = try series("overnight")
        await m.load(source: Source(answer: s, fails: false), vehicleID: "a")
        XCTAssertEqual(m.chipSince(vehicleID: "a", state: state, late: false), s.parkedSince)
        // Another truck selected: nothing of this one's, and the wait starts over.
        XCTAssertNil(m.chipSince(vehicleID: "b", state: state, late: false))
        // Back from the background: the view clears the model and resets late, so the chip holds its time again.
        m.clear()
        XCTAssertNil(m.chipSince(vehicleID: "a", state: state, late: false))
        // A failed read ends the wait with the state's time.
        await m.load(source: Source(answer: nil, fails: true), vehicleID: "a")
        XCTAssertEqual(m.chipSince(vehicleID: "a", state: state, late: false), state)
    }
}
