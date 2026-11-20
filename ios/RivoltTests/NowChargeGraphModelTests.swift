import XCTest
@testable import Rivolt

/// The Now card's charging graph numbers (design_preview key now_charge_graph), as the web's: labels equal the ring, the estimate is capped, too little is "collecting".
@MainActor
final class NowChargeGraphModelTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func series(minutes: Int, from: Double = 41, to: Double = 59, kw: Double = 11) -> ChargeCurve.Series {
        let pts = stride(from: 0, through: minutes * 60, by: 30).map { s in
            (at: t0.addingTimeInterval(Double(s)), f: Double(s) / Double(minutes * 60))
        }
        return ChargeCurve.Series(soc: pts.map { .init(at: $0.at, value: from + (to - from) * $0.f) }, power: pts.map { .init(at: $0.at, value: kw) })
    }

    func testLabelsAreTheRealStartAndTheRing() throws {
        let m = try XCTUnwrap(NowChargeGraphModel.make(series: series(minutes: 40), startPct: 41, nowPct: 59, target: 80, etaHours: 2, now: t0.addingTimeInterval(2400)))
        XCTAssertEqual(m.startShown, 41)
        XCTAssertEqual(m.nowShown, 59)
        XCTAssertEqual(m.change, "+18%")
    }

    /// The newest sample is 40 s old and the finish is 30.006 min away: the pill says 31 minutes from now, as the chip and the slot do.
    func testThePillCountsWholeMinutesFromNowNotFromTheLastSample() throws {
        let now = t0.addingTimeInterval(2440)
        let minutes = 31
        let m = try XCTUnwrap(NowChargeGraphModel.make(series: series(minutes: 40), startPct: 41, nowPct: 59, target: 80, etaHours: Double(minutes) / 60, now: now))
        let when = now.addingTimeInterval(Double(minutes) * 60).formatted(date: .omitted, time: .shortened)
        XCTAssertEqual(m.estimate?.pill, "80% · \(when)")
    }

    /// The sample says 11 kW, the card's live reading 12: one number, the card's.
    func testTheLegendNamesTheLivePower() throws {
        let m = try XCTUnwrap(NowChargeGraphModel.make(series: series(minutes: 40, kw: 11), startPct: 41, nowPct: 59, target: 80, etaHours: 1, powerKw: 12, now: t0.addingTimeInterval(2400)))
        XCTAssertEqual(m.powerNow, "12")
        XCTAssertTrue(m.spoken.contains("12 kilowatts"))
    }

    func testTooLittleIsCollecting() {
        XCTAssertNil(NowChargeGraphModel.make(series: series(minutes: 1), startPct: 41, nowPct: 42, target: 80, etaHours: 2, now: t0.addingTimeInterval(60)))
        XCTAssertNil(NowChargeGraphModel.make(series: ChargeCurve.Series(), startPct: nil, nowPct: 42, target: 80, etaHours: 2, now: t0))
    }

    func testTheEstimateTakesAtMostEightyPercentOfTheSessionWidth() throws {
        let m = try XCTUnwrap(NowChargeGraphModel.make(series: series(minutes: 40), startPct: 41, nowPct: 59, target: 80, etaHours: 20, now: t0.addingTimeInterval(2400)))
        // The session is 1 / 1.8 of the plot's width, however far away the finish is.
        XCTAssertEqual(m.last.x, 1 / 1.8, accuracy: 0.001)
        XCTAssertEqual(m.estimate?.end.x, 1)
    }

    func testNoFinishTimeNoEstimate() throws {
        let m = try XCTUnwrap(NowChargeGraphModel.make(series: series(minutes: 40), startPct: 41, nowPct: 59, target: 80, etaHours: nil, now: t0.addingTimeInterval(2400)))
        XCTAssertNil(m.estimate)
        XCTAssertEqual(m.last.x, 1, accuracy: 0.001)
    }

    func testPowerFormatMatchesTheSlot() {
        XCTAssertEqual(NowChargeGraphModel.kw(4.8), "4.8")
        XCTAssertEqual(NowChargeGraphModel.kw(11.1), "11")
        XCTAssertEqual(NowChargeGraphModel.powerMax(11), 12)
        XCTAssertEqual(NowChargeGraphModel.powerMax(190), 300)
    }

    func testPackReadingIsHiddenWhenOlderThanFifteenMinutes() {
        let model = NowChargeSeriesModel()
        let json = #"[{"At":"2026-11-19T10:00:00Z","BatteryLevelPct":40,"ChargerPowerKW":11,"pack_temp_avg_c":25}]"#
        let rows = try! JSON.decoder().decode([ChargeSample].self, from: Data(json.utf8))
        let at = rows[0].at
        model.apply(rows, startedAt: at, now: at.addingTimeInterval(60))
        XCTAssertEqual(model.packTempC(now: at.addingTimeInterval(14 * 60)), 25)
        XCTAssertNil(model.packTempC(now: at.addingTimeInterval(16 * 60)))
    }

    private struct Source: ChargeSamplesSource {
        let rows: [ChargeSample]?
        func chargeSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [ChargeSample] {
            guard let rows else { throw URLError(.timedOut) }
            return rows
        }
    }

    func testAnotherTrucksOrAnEarlierChargesRowsAreNeverDrawn() async throws {
        let json = #"[{"At":"2026-11-19T10:00:00Z","BatteryLevelPct":40,"ChargerPowerKW":11,"pack_temp_avg_c":25},{"At":"2026-11-19T10:01:00Z","BatteryLevelPct":41,"ChargerPowerKW":11,"pack_temp_avg_c":25}]"#
        let rows = try JSON.decoder().decode([ChargeSample].self, from: Data(json.utf8))
        let start = rows[0].at
        let model = NowChargeSeriesModel()
        XCTAssertFalse(model.matches(vehicleID: "a", startedAt: start), "nothing read yet")
        await model.load(source: Source(rows: rows), vehicleID: "a", startedAt: start, now: start.addingTimeInterval(90))
        XCTAssertTrue(model.matches(vehicleID: "a", startedAt: start))
        XCTAssertEqual(model.packTempC(now: start.addingTimeInterval(90)), 25)
        // Another truck, and the read fails: the first truck's rows and pack reading are gone, and the card has nothing to draw.
        await model.load(source: Source(rows: nil), vehicleID: "b", startedAt: start, now: start.addingTimeInterval(90))
        XCTAssertFalse(model.matches(vehicleID: "a", startedAt: start))
        XCTAssertTrue(model.series.soc.isEmpty)
        XCTAssertNil(model.packTempC(now: start.addingTimeInterval(90)))
        XCTAssertNil(model.startPct)
        // The same truck's next charge is its own too.
        await model.load(source: Source(rows: rows), vehicleID: "b", startedAt: start, now: start.addingTimeInterval(90))
        await model.load(source: Source(rows: nil), vehicleID: "b", startedAt: start.addingTimeInterval(3600), now: start.addingTimeInterval(3700))
        XCTAssertFalse(model.matches(vehicleID: "b", startedAt: start))
        XCTAssertTrue(model.series.soc.isEmpty)
    }

    func testTheSoftChipDropsTheFinishTimeOnANarrowCard() {
        let chip = "Charging · 11 kW · done by 3:24 PM"
        XCTAssertEqual(NowCard.softChip(chip, wide: false), "Charging · 11 kW")
        XCTAssertEqual(NowCard.softChip(chip, wide: true), chip)
    }

    func testSoftSlotLeavesOutReachesTargetAndPluggedIn() throws {
        // The slot's charging figures with `soft`: no "Reaches" line, no "· target", no "Plugged in" row.
        let iso = ISO8601DateFormatter().string(from: t0)
        let json = #"{"version":1,"at":"\#(iso)","connection":"live","state":"charging","failed_sleeps_6h":0,"charge":{"started_at":"\#(iso)","soc_start":41,"peak_kw":11}}"#
        let now = try JSON.decoder().decode(NowSnapshot.self, from: Data(json.utf8))
        let input = NowSummary.Input(socPct: 59, rangeKm: 250, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charging_active", chargerPowerKw: 11, at: t0, packKwh: 100, speedKph: nil)
        let summary = NowSummary(input: input, now: now, clock: t0.addingTimeInterval(2400))
        for soft in [false, true] {
            let m = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, home: nil, soft: soft, clock: t0.addingTimeInterval(2400)))
            guard case .figures(_, _, let sub, let small, let stats) = m.content else { return XCTFail("figures") }
            XCTAssertEqual(sub == nil, soft)
            XCTAssertEqual(small?.contains("target") ?? false, !soft)
            XCTAssertEqual(stats.contains { $0.label == "Plugged in" }, !soft)
        }
    }
}
