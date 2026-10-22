import SwiftUI
import XCTest
@testable import Rivolt

final class ChargeCurveTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func pt(_ s: Double, _ v: Double) -> ChargeCurve.Pt { .init(at: t0.addingTimeInterval(s), value: v) }

    private func sample(_ s: Double, soc: Double = 50, kw: Double = 0, pack: Double? = nil, lo: Double? = nil, hi: Double? = nil) -> ChargeSample {
        ChargeSample(at: t0.addingTimeInterval(s), batteryLevelPct: soc, chargerPowerKw: kw, packTempAvgC: pack, packTempMinC: lo, packTempMaxC: hi)
    }

    func testSamplesDecodeOnlyTheNineFieldsAndNeverAPosition() throws {
        let json = #"[{"VehicleID":"veh-fixture","At":"2026-10-20T15:01:00Z","Lat":32.775,"Lon":-96.797,"LocationFixAt":"2026-10-20T15:00:58Z","BatteryLevelPct":61.5,"ChargerPowerKW":11.2,"ChargeLimitPct":80,"InsideTempC":21,"OutsideTempC":12,"OdometerMi":12000,"SpeedMph":0,"pack_temp_avg_c":30.5,"pack_temp_min_c":29,"pack_temp_max_c":32,"altitude_m":150}]"#
        let s = try JSON.decoder().decode([ChargeSample].self, from: Data(json.utf8))
        XCTAssertEqual(s[0].batteryLevelPct, 61.5)
        XCTAssertEqual(s[0].chargerPowerKw, 11.2)
        XCTAssertEqual(s[0].packTempAvgC, 30.5)
        XCTAssertEqual(Mirror(reflecting: s[0]).children.compactMap(\.label),
                       ["at", "batteryLevelPct", "chargerPowerKw", "packTempAvgC", "packTempMinC", "packTempMaxC",
                        "chargeLimitPct", "outsideTempC", "insideTempC"])
        XCTAssertEqual(s[0].chargeLimitPct, 80)
        XCTAssertEqual(s[0].outsideTempC, 12)
        XCTAssertEqual(s[0].insideTempC, 21)
        let bare = #"[{"At":"2026-10-20T15:01:00Z","BatteryLevelPct":50,"ChargerPowerKW":0}]"#
        XCTAssertNil(try JSON.decoder().decode([ChargeSample].self, from: Data(bare.utf8))[0].packTempAvgC, "older rows carry no pack temperature")
    }

    func testSmoothingKeepsAFlatLineFlatAndShavesASpike() {
        let flat = (0..<9).map { pt(Double($0) * 10, 5) }
        for (a, b) in zip(ChargeCurve.smooth(flat, sigma: 30), flat) { XCTAssertEqual(a.value, b.value, accuracy: 1e-9) }
        var spiky = flat
        spiky[4] = pt(40, 50)
        let smooth = ChargeCurve.smooth(spiky, sigma: 30)
        XCTAssertLessThan(smooth[4].value, 50)
        XCTAssertGreaterThan(smooth[4].value, 5)
        XCTAssertEqual(smooth.map(\.at), spiky.map(\.at))
        XCTAssertEqual(ChargeCurve.smooth([pt(0, 1), pt(10, 9)], sigma: 30), [pt(0, 1), pt(10, 9)], "two points are left alone")
    }

    func testThePowerAxisSnapsToTheChargerClasses() {
        XCTAssertEqual(ChargeCurve.powerAxisMax(peak: 7.2, smoothed: []), 12)
        XCTAssertEqual(ChargeCurve.powerAxisMax(peak: 11.5, smoothed: []), 24, "a peak within 8% of the tier moves up")
        XCTAssertEqual(ChargeCurve.powerAxisMax(peak: 142, smoothed: []), 200)
        XCTAssertEqual(ChargeCurve.powerAxisMax(peak: 0, smoothed: []), 12)
        XCTAssertEqual(ChargeCurve.powerAxisMax(peak: 10, smoothed: [pt(0, 130)]), 200, "the smoothed line can be higher than the row's peak")
        XCTAssertEqual(ChargeCurve.powerAxisMax(peak: 500, smoothed: []), 600)
    }

    func testTheSeriesKeepsTheWindowAndDropsWhatWasNotReported() {
        let samples = [
            sample(-120, soc: 40, kw: 5),                       // before the start
            sample(-30, soc: 41, kw: 0),                        // before the start too
            sample(0, soc: 41, kw: 7, pack: 25, lo: 24, hi: 27),
            sample(60, soc: 0, kw: 7, pack: 0),                 // no level, no temperature
            sample(120, soc: 43, kw: 0, pack: 26, lo: 24, hi: 31),
            sample(600, soc: 80, kw: 7),
            sample(661, soc: 81, kw: 7),                        // after end + 60 s
        ]
        let s = ChargeCurve.series(samples, from: t0, to: t0.addingTimeInterval(600))
        XCTAssertEqual(s.soc.map(\.at), [0, 120, 600].map { t0.addingTimeInterval($0) })
        XCTAssertEqual(s.power.count, 3)
        XCTAssertEqual(s.packAvgC.count, 2)
        XCTAssertEqual(s.packSpreadC, 7)
        XCTAssertTrue(ChargeCurve.series([], from: t0, to: t0).soc.isEmpty)
    }

    func testTheBatteryAxisIsRoundedOutToWholeTicksThatCoverTheData() {
        func ax(_ s: ChargeCurve.Series, _ a: Double, _ b: Double, kw: Double = 142) -> ChargeCurve.Axes { ChargeCurve.axes(s, startPct: a, endPct: b, peakKw: kw) }
        let dc = ChargeCurve.Series(soc: [pt(0, 18), pt(60, 74)])
        XCTAssertEqual(ax(dc, 18, 74).ticks, [0, 20, 40, 60, 80], "13 to 79 rounds out to 0 to 80, the top and the bottom labelled")
        let mid = ChargeCurve.Series(soc: [pt(0, 41), pt(60, 80)])
        XCTAssertEqual(ax(mid, 41, 80).ticks, [20, 40, 60, 80, 100], "36 to 85: the finest step that leaves three or five ticks")
        let high = ChargeCurve.Series(soc: [pt(0, 88), pt(60, 98)])
        XCTAssertEqual(ax(high, 88, 98).ticks, [80, 85, 90, 95, 100])
        let home = ChargeCurve.Series(soc: [pt(0, 55), pt(60, 85)])
        XCTAssertEqual(ax(home, 55, 85).ticks, [50, 60, 70, 80, 90])
        for a in [ax(dc, 18, 74), ax(mid, 41, 80), ax(high, 88, 98), ax(home, 55, 85)] {
            XCTAssertTrue([3, 5].contains(a.ticks.count), "three or five gridlines, so the kW labels are whole numbers")
        }
        // The line can run past the row's own levels: the domain covers it.
        let past = ChargeCurve.Series(soc: [pt(0, 41), pt(60, 83)])
        XCTAssertGreaterThanOrEqual(ax(past, 41, 70).socHi, 83)
        XCTAssertEqual(ax(.init(soc: [pt(0, 41), pt(60, 80)]), 0, 0).socLo, 40, "unknown levels: the data's own range")
        let axes = ax(dc, 18, 74)
        XCTAssertEqual(ChargeCurve.unit(soc: 0, axes), 0)
        XCTAssertEqual(ChargeCurve.unit(soc: 80, axes), 1)
        XCTAssertEqual(ChargeCurve.unit(soc: 120, axes), 1, "never drawn outside the plot")
        XCTAssertEqual(ChargeCurve.unit(power: 100, axes), 0.5)
        XCTAssertEqual(ChargeCurve.unit(temp: 20, range: 20...30), 0.05, accuracy: 1e-9)
        XCTAssertEqual(ChargeCurve.unit(temp: 30, range: 20...30), 0.95, accuracy: 1e-9)
        XCTAssertEqual(ax(dc, 18, 74).powerMax, 200)
        // The kW labels at the battery gridlines: 0, 50, 100, 150, 200.
        XCTAssertEqual(axes.ticks.map { ChargeCurve.unit(soc: $0, axes) * axes.powerMax }, [0, 50, 100, 150, 200])
    }

    func testAWholePercentStaircaseBecomesARamp() {
        // 55% for 10 samples, then 56% for 10, 57% for 10: flat runs, a jump each.
        var pts: [ChargeCurve.Pt] = []
        for k in 0..<3 { for i in 0..<10 { pts.append(pt(Double(k * 100 + i * 10), 55 + Double(k))) } }
        let ramp = ChargeCurve.deStair(pts)
        XCTAssertEqual(ramp.count, 5, "the first sample, a point in the middle of each run, the last sample")
        XCTAssertEqual(ramp.first, pts.first)
        XCTAssertEqual(ramp.last, pts.last)
        XCTAssertEqual(ramp[2].at, t0.addingTimeInterval(145), "the middle of the 56% run")
        XCTAssertEqual(ramp.map(\.value), [55, 55, 56, 57, 57])
        XCTAssertEqual(ChargeCurve.deStair([pt(0, 1), pt(5, 2)]), [pt(0, 1), pt(5, 2)])
    }

    func testARealPlateauKeepsItsEndsAndIsDrawnFlat() {
        // 80% then 81% in short runs, then 85% held an hour while still plugged in (a sample a minute): a plateau at the end.
        var tail: [ChargeCurve.Pt] = []
        for i in 0..<20 { tail.append(pt(Double(i) * 10, 80)) }
        for i in 0..<10 { tail.append(pt(200 + Double(i) * 10, 81)) }
        for i in 0...60 { tail.append(pt(300 + Double(i) * 60, 85)) }
        let r = ChargeCurve.deStair(tail)
        XCTAssertEqual(Array(r.suffix(2)), [pt(300, 85), pt(3_900, 85)], "both ends of the hour, nothing ramping across it")
        XCTAssertEqual(r.filter { $0.value == 85 }.count, 2)
        XCTAssertEqual(r.last, tail.last)

        // A plateau in the middle: 69%, 70% for half an hour, 71%.
        var mid: [ChargeCurve.Pt] = []
        for i in 0..<10 { mid.append(pt(Double(i) * 10, 69)) }
        for i in 0...30 { mid.append(pt(100 + Double(i) * 60, 70)) }
        for i in 0..<10 { mid.append(pt(1_960 + Double(i) * 10, 71)) }
        let m = ChargeCurve.deStair(mid)
        XCTAssertEqual(m.filter { $0.value == 70 }, [pt(100, 70), pt(1_900, 70)], "flat between its first and last sample")
        XCTAssertEqual(m.first, mid.first)
        XCTAssertEqual(m.last, mid.last)
        XCTAssertEqual(m.map(\.at), m.map(\.at).sorted(), "still in time order")

        // A flat series is left with its two ends.
        XCTAssertEqual(ChargeCurve.deStair([pt(0, 50), pt(10, 50), pt(20, 50)]), [pt(0, 50), pt(20, 50)])
    }

    func testTheReadoutFindsTheNearestSample() {
        let pts = [pt(0, 10), pt(60, 20), pt(120, 30)]
        XCTAssertEqual(ChargeCurve.nearest(pts, to: t0.addingTimeInterval(50)), 20)
        XCTAssertEqual(ChargeCurve.nearest(pts, to: t0.addingTimeInterval(-500)), 10)
        XCTAssertNil(ChargeCurve.nearest([], to: t0))
        XCTAssertEqual(ChargeCurve.temperature(30, metric: true), 30)
        XCTAssertEqual(ChargeCurve.temperature(30, metric: false), 86, accuracy: 1e-9)
    }

    func testTheCurveReadsAsSentences() {
        let s = ChargeCurve.Series(soc: [pt(0, 41.2), pt(2_100, 79.6)], power: [pt(300, 142)])
        let words = SpokenText.words(ChargeCurve.spoken(s, peakKw: 150))
        XCTAssertEqual(words, "Battery from 41 percent to 80 percent over 35 minutes. Peak power 150 kilowatts.")
        XCTAssertEqual(ChargeCurve.spoken(.init(), peakKw: 0), "No battery readings.")
    }

    // MARK: The model

    private func charge(start: Date, end: Date) throws -> ActivityCharge {
        let json = """
        {"id":"c1","vehicle_id":"veh-1","started_at":"\(ISO8601DateFormatter().string(from: start))","ended_at":"\(ISO8601DateFormatter().string(from: end))",
         "energy_added_kwh":20,"start_soc_pct":41,"end_soc_pct":80,"max_power_kw":142,"cost":0,"currency":"USD"}
        """
        return try JSON.decoder().decode(ActivityCharge.self, from: Data(json.utf8))
    }

    func testTheWindowIsAMinuteEitherSideAndRunsToNowWhileTheChargeIsOpen() throws {
        let done = try charge(start: t0, end: t0.addingTimeInterval(1_800))
        let w = ChargeCurveModel.window(for: done, now: t0.addingTimeInterval(86_400))
        XCTAssertEqual(w.since, t0.addingTimeInterval(-60))
        XCTAssertEqual(w.until, t0.addingTimeInterval(1_860))
        let open = try charge(start: t0, end: t0.addingTimeInterval(1_800))
        let now = t0.addingTimeInterval(1_800 + 300)
        XCTAssertEqual(ChargeCurveModel.window(for: open, now: now).until, now.addingTimeInterval(60), "ended 5 minutes ago: still going")
    }

    @MainActor
    func testTheModelAsksForTheTrucksWindowAndSaysWhenItFails() async throws {
        let c = try charge(start: t0, end: t0.addingTimeInterval(1_800))
        let source = FakeSamples(result: .success([sample(0, soc: 41, kw: 7), sample(600, soc: 50, kw: 7), sample(1_200, soc: 60, kw: 7)]))
        let model = ChargeCurveModel()
        await model.load(source: source, charge: c, now: t0.addingTimeInterval(86_400))
        XCTAssertEqual(source.calls.count, 1)
        XCTAssertEqual(source.calls[0].vehicleID, "veh-1")
        XCTAssertEqual(source.calls[0].limit, 2000)
        XCTAssertEqual(source.calls[0].since, t0.addingTimeInterval(-60))
        guard case .ready(let series) = model.phase else { return XCTFail("not ready: \(model.phase)") }
        XCTAssertEqual(series.soc.count, 3)

        let failing = ChargeCurveModel()
        await failing.load(source: FakeSamples(result: .failure(URLError(.badServerResponse))), charge: c, now: t0.addingTimeInterval(86_400))
        XCTAssertEqual(failing.phase, .failed)
    }
}

private final class FakeSamples: ChargeSamplesSource, @unchecked Sendable {
    struct Call { let since: Date; let until: Date; let vehicleID: String; let limit: Int }
    private let result: Result<[ChargeSample], Error>
    private let lock = NSLock()
    private var _calls: [Call] = []
    var calls: [Call] { lock.withLock { _calls } }

    private func record(_ call: Call) { lock.withLock { _calls.append(call) } }

    init(result: Result<[ChargeSample], Error>) { self.result = result }

    func chargeSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [ChargeSample] {
        record(Call(since: since, until: until, vehicleID: vehicleID, limit: limit))
        return try result.get()
    }
}
