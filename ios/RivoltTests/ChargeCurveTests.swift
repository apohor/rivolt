import SwiftUI
import XCTest
@testable import Rivolt

final class ChargeCurveTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func pt(_ s: Double, _ v: Double) -> ChargeCurve.Pt { .init(at: t0.addingTimeInterval(s), value: v) }

    private func sample(_ s: Double, soc: Double = 50, kw: Double = 0, pack: Double? = nil, lo: Double? = nil, hi: Double? = nil) -> ChargeSample {
        ChargeSample(at: t0.addingTimeInterval(s), batteryLevelPct: soc, chargerPowerKw: kw, packTempAvgC: pack, packTempMinC: lo, packTempMaxC: hi)
    }

    func testSamplesDecodeOnlyTheSixFieldsAndNeverAPosition() throws {
        let json = #"[{"VehicleID":"veh-fixture","At":"2026-10-20T15:01:00Z","Lat":32.775,"Lon":-96.797,"LocationFixAt":"2026-10-20T15:00:58Z","BatteryLevelPct":61.5,"ChargerPowerKW":11.2,"ChargeLimitPct":80,"InsideTempC":21,"OutsideTempC":12,"OdometerMi":12000,"SpeedMph":0,"pack_temp_avg_c":30.5,"pack_temp_min_c":29,"pack_temp_max_c":32,"altitude_m":150}]"#
        let s = try JSON.decoder().decode([ChargeSample].self, from: Data(json.utf8))
        XCTAssertEqual(s[0].batteryLevelPct, 61.5)
        XCTAssertEqual(s[0].chargerPowerKw, 11.2)
        XCTAssertEqual(s[0].packTempAvgC, 30.5)
        XCTAssertEqual(Mirror(reflecting: s[0]).children.compactMap(\.label),
                       ["at", "batteryLevelPct", "chargerPowerKw", "packTempAvgC", "packTempMinC", "packTempMaxC"])
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
        XCTAssertEqual(ChargeCurve.powerAxisMax(peak: 11.5, smoothed: []), 25, "a peak within 8% of the tier moves up")
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

    func testTheBatteryAxisFollowsTheSessionOrTheData() {
        let s = ChargeCurve.Series(soc: [pt(0, 41), pt(60, 80)])
        XCTAssertEqual(ChargeCurve.axes(s, startPct: 41, endPct: 80, peakKw: 142).socLo, 36)
        XCTAssertEqual(ChargeCurve.axes(s, startPct: 41, endPct: 80, peakKw: 142).socHi, 85)
        XCTAssertEqual(ChargeCurve.axes(s, startPct: 41, endPct: 80, peakKw: 142).powerMax, 200)
        XCTAssertEqual(ChargeCurve.axes(s, startPct: 0, endPct: 0, peakKw: 0).socLo, 41, "unknown levels: the data's own range")
        XCTAssertEqual(ChargeCurve.axes(s, startPct: 98, endPct: 100, peakKw: 7).socHi, 100)
        let axes = ChargeCurve.axes(s, startPct: 41, endPct: 80, peakKw: 142)
        XCTAssertEqual(ChargeCurve.unit(soc: 36, axes), 0)
        XCTAssertEqual(ChargeCurve.unit(soc: 85, axes), 1)
        XCTAssertEqual(ChargeCurve.unit(soc: 120, axes), 1, "never drawn outside the plot")
        XCTAssertEqual(ChargeCurve.unit(power: 100, axes), 0.5)
        XCTAssertEqual(ChargeCurve.unit(temp: 20, range: 20...30), 0.05, accuracy: 1e-9)
        XCTAssertEqual(ChargeCurve.unit(temp: 30, range: 20...30), 0.95, accuracy: 1e-9)
    }

    func testTheAxisTicksAreRoundNumbersInsideTheRange() {
        func ticks(_ lo: Double, _ hi: Double) -> [Double] { ChargeCurve.socTicks(.init(socLo: lo, socHi: hi, powerMax: 200)) }
        XCTAssertEqual(ticks(36, 85), [40, 50, 60, 70, 80])
        XCTAssertEqual(ticks(13, 79), [20, 40, 60])
        XCTAssertEqual(ticks(50, 90), [50, 60, 70, 80, 90])
        XCTAssertEqual(ticks(88, 100), [90, 95, 100])
        XCTAssertEqual(ticks(0, 100), [0, 25, 50, 75, 100])
        XCTAssertEqual(ChargeCurve.powerTicks(.init(socLo: 0, socHi: 100, powerMax: 25)), [0, 12.5, 25])
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
