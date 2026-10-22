import XCTest
@testable import Rivolt

/// The web's power.test.ts and driveTimelineData.test.ts, case for case, plus the moments and modes the timeline
/// shows.
final class DriveTimelineMathTests: XCTestCase {
    private typealias M = DriveTimelineMath
    private let base = Date(timeIntervalSince1970: 1_785_585_600) // 2026-08-01 12:00 UTC

    private func samples(_ pts: [(Double, Double)], mode: String? = nil, gear: String = "D") -> [DriveSample] {
        pts.map { off, mph in
            DriveSample(at: base.addingTimeInterval(off), lat: 32.7767, lon: -96.797, speedMph: mph, shiftState: gear,
                        locationFixAt: nil, driveMode: mode)
        }
    }

    private func analyze(_ s: [DriveSample], kwh: Double) -> (drawKwh: Double, regenKwh: Double, pct: Double) {
        M.regen(M.power(s, elev: M.elevPts(s), energyKwh: kwh))
    }

    func testPlausibleTotalsOnCleanSamples() {
        var pts: [(Double, Double)] = (0..<36).map { i -> (Double, Double) in (Double(i) * 5, 55) }
        for i in 1...4 {
            let d = Double(i)
            pts.append((180 + d * 5, 55 - d * 13.75))
        }
        let pa = analyze(samples(pts), kwh: 1.2)
        XCTAssertGreaterThan(pa.drawKwh, 0)
        XCTAssertLessThanOrEqual(pa.regenKwh, pa.drawKwh)
        XCTAssertTrue((0...100).contains(pa.pct))
    }

    func testNearDuplicateSamplesAreNotIntegrated() {
        let pts: [(Double, Double)] = (0..<20).map { i -> (Double, Double) in (Double(i) * 5, 55) }
        let clean = analyze(samples(pts), kwh: 0.8)
        var poisoned: [(Double, Double)] = Array(pts.prefix(11))
        poisoned.append((50.001, 0))
        poisoned += Array(pts.dropFirst(11))
        let got = analyze(samples(poisoned), kwh: 0.8)
        XCTAssertLessThanOrEqual(got.regenKwh, got.drawKwh)
        XCTAssertLessThan(got.regenKwh, clean.regenKwh + 0.5)
    }

    func testGapsAreNotIntegratedAcross() {
        let contiguous: [(Double, Double)] = (0..<12).map { i -> (Double, Double) in (Double(i) * 5, 45) }
        var gapped: [(Double, Double)] = Array(contiguous.prefix(6))
        for i in 6..<12 { gapped.append((1200 + Double(i) * 5, 45)) }
        let a = analyze(samples(contiguous), kwh: 0.4), b = analyze(samples(gapped), kwh: 0.4)
        XCTAssertLessThan(b.regenKwh, a.regenKwh + 0.05)
        XCTAssertLessThan(b.drawKwh, a.drawKwh * 1.5)
    }

    func testDiscontinuitiesAreMarked() {
        let s = samples([(0, 40), (5, 42), (10, 41), (600, 44), (605, 43), (610, 45)])
        let out = M.power(s, elev: M.elevPts(s), energyKwh: 0)
        XCTAssertGreaterThanOrEqual(out.count, 4)
        XCTAssertTrue(out[0].gap)
        XCTAssertEqual(out.dropFirst().filter(\.gap).count, 1)
    }

    func testSubSecondAndOverLongIntervalsAreDropped() {
        let s = samples([(0, 30), (0.0001, 60), (5, 30), (10, 31), (500, 33), (505, 32)])
        for p in M.power(s, elev: M.elevPts(s), energyKwh: 0) { XCTAssertLessThan(abs(p.y), 1e4) }
    }

    private func pts(_ ys: [Double], step: Double = 60_000) -> [M.Pt] {
        ys.enumerated().map { M.Pt(x: 1_700_000_000_000 + Double($0) * step, y: $1) }
    }

    func testLowestSoCOnlyWhenItIsNotTheEnd() {
        let falling = pts([80, 78, 76, 74, 72, 70, 68, 66])
        XCTAssertFalse(M.moments(speed: pts(falling.map { _ in 30 }), soc: falling, power: []).contains { $0.label == "Lowest SoC" })
        let dip = pts([80, 70, 60, 50, 55, 60, 64, 66])
        XCTAssertTrue(M.moments(speed: pts(dip.map { _ in 30 }), soc: dip, power: []).contains { $0.label == "Lowest SoC" })
    }

    func testHighwayEntryTopSpeedHardBrakeAndMaxRegen() {
        // 10 s steps: city, onto the highway for a minute, a hard brake mid-drive, then on to the end.
        let speed = pts([20, 30, 35, 58, 60, 62, 64, 66, 40, 45, 50, 30, 0], step: 10_000)
        let power = [M.PowerPt(x: speed[8].x, y: -35), M.PowerPt(x: speed[3].x, y: 80)]
        let m = M.moments(speed: speed, soc: [], power: power)
        XCTAssertEqual(m.first { $0.label == "Highway entry" }?.ms, speed[3].x)
        XCTAssertEqual(m.first { $0.label == "Top speed" }?.detail(metric: false), "66 mph")
        XCTAssertEqual(m.first { $0.label == "Top speed" }?.detail(metric: true), "106 km/h")
        XCTAssertNil(m.first { $0.label == "Hard brake" }, "a 26 mph drop in 10 s is -2.6 mph/s: not hard")
        XCTAssertEqual(m.first { $0.label == "Max regen" }?.detail(metric: false), "-35 kW recovered")
        // A 26 mph drop in 2 s is.
        var sharp = speed
        sharp[8] = M.Pt(x: speed[7].x + 2_000, y: 40)
        XCTAssertEqual(M.moments(speed: sharp, soc: [], power: []).first { $0.label == "Hard brake" }?.detail(metric: false), "-13.0 mph/s deceleration")
    }

    func testCloseMomentsKeepTheStrongerTone() {
        let speed = pts([20, 58, 60, 60, 60, 60], step: 10_000)
        // Highway entry (info) and Max regen (success) 3 s apart: the regen stays.
        let m = M.moments(speed: speed, soc: [], power: [M.PowerPt(x: speed[1].x + 3_000, y: -30)])
        XCTAssertFalse(m.contains { $0.label == "Highway entry" })
        XCTAssertTrue(m.contains { $0.label == "Max regen" })
    }

    func testDriveModes() {
        XCTAssertEqual(M.classifyMode(samples([(0, 30)], mode: "everyday")[0]), .allPurpose)
        XCTAssertEqual(M.classifyMode(samples([(0, 30)], mode: "distance")[0]), .conserve)
        XCTAssertEqual(M.classifyMode(samples([(0, 30)], mode: "Sport")[0]), .sport)
        XCTAssertEqual(M.classifyMode(samples([(0, 30)], mode: "offroad_auto")[0]), .other)
        // No mode recorded: the gear.
        XCTAssertEqual(M.classifyMode(samples([(0, 30)])[0]), .allPurpose)
        XCTAssertEqual(M.classifyMode(samples([(0, 30)], gear: "R")[0]), .other)
        let s = samples([(0, 30), (10, 30), (20, 30)], mode: "sport") + samples([(30, 30), (40, 30)], mode: "distance")
        let segs = M.modeSegments(s, start: M.ms(base), end: M.ms(base) + 50_000)
        XCTAssertEqual(segs.map(\.mode), [.sport, .conserve])
        XCTAssertEqual(segs.last?.x1, M.ms(base) + 50_000)
        XCTAssertEqual(M.Mode.allPurpose.label, "All-Purpose")
    }

    func testSmoothingKeepsAFlatLineAndFewPoints() {
        let flat = pts([5, 5, 5, 5, 5])
        XCTAssertEqual(M.smoothGaussianTime(flat, sigmaMs: 30_000).map(\.y), [5, 5, 5, 5, 5])
        XCTAssertEqual(M.smoothGaussianTime(Array(flat.prefix(2)), sigmaMs: 30_000), Array(flat.prefix(2)))
        // A missing battery reading isn't 0 %.
        var s = samples([(0, 30), (10, 30)])
        s[0].batteryLevelPct = 70
        s[1].batteryLevelPct = 0
        XCTAssertEqual(M.socPts(s).map(\.y), [70])
    }
}
