import XCTest
@testable import Rivolt

/// The Efficiency and Elevation colour modes, with the web's metrics.test.ts cases: a made-up straight drive of samples about 0.1 mi apart,
/// the battery falling faster on the climbs (a wave), altitude rising.
final class RouteMetricsTests: XCTestCase {
    private func socWave(_ i: Int) -> Double { 80 - (0.083 * Double(i) + 0.06 * 9 * (1 - cos(Double(i) / 9))) }

    private func drive(_ n: Int, soc: ((Int) -> Double?)? = nil, alt: ((Int) -> Double?)? = nil) -> [RouteMetrics.Sample] {
        (0..<n).map { i in
            RouteMetrics.Sample(lat: 10 + Double(i) * 0.0015, lon: 10, alt: alt.map { $0(i) } ?? (100 + Double(i) * 2),
                                soc: soc.map { $0(i) } ?? socWave(i), odo: 5000 + Double(i) * 0.1)
        }
    }

    func testALongDriveWithBatteryAndAltitudeHasBothModes() {
        let s = RouteMetrics.series(drive(120), packKWh: 135)
        XCTAssertNotNil(s[.efficiency])
        XCTAssertNotNil(s[.elevation])
    }

    func testEfficiencyNeedsFiveMilesAndAFivePointBatteryChange() {
        let short = drive(30), flat = drive(120, soc: { _ in 60 }), ok = drive(120)
        XCTAssertNil(RouteMetrics.efficiencySeries(short, packKWh: 135, cum: RouteMetrics.cumulativeMiles(short)), "3 mi")
        XCTAssertNil(RouteMetrics.efficiencySeries(flat, packKWh: 135, cum: RouteMetrics.cumulativeMiles(flat)), "battery did not change")
        XCTAssertNotNil(RouteMetrics.efficiencySeries(ok, packKWh: 135, cum: RouteMetrics.cumulativeMiles(ok)))
    }

    func testEfficiencyIsMilesPerKWhFromTheBattery() throws {
        let s = drive(121, soc: { 80 - Double($0) * 0.12 })
        let eff = try XCTUnwrap(RouteMetrics.efficiencySeries(s, packKWh: 135, cum: RouteMetrics.cumulativeMiles(s)))
        // 0.1 mi per (0.0012 * 135) kWh = 0.617 mi/kWh
        XCTAssertGreaterThan(eff[60], 0.55)
        XCTAssertLessThan(eff[60], 0.7)
    }

    func testASteadyDrainIsHiddenAsAModeAndAVaryingOneOffered() {
        XCTAssertNil(RouteMetrics.series(drive(121, soc: { 80 - Double($0) * 0.12 }), packKWh: 135)[.efficiency])
        XCTAssertNotNil(RouteMetrics.series(drive(121), packKWh: 135)[.efficiency])
    }

    func testEfficiencyIsWithheldWithoutAPackSizeOrBatteryValues() {
        let s = drive(120)
        XCTAssertNil(RouteMetrics.efficiencySeries(s, packKWh: nil, cum: RouteMetrics.cumulativeMiles(s)))
        var noSoc = s
        noSoc[10].soc = nil
        XCTAssertNil(RouteMetrics.efficiencySeries(noSoc, packKWh: 135, cum: RouteMetrics.cumulativeMiles(noSoc)))
    }

    func testElevationNeedsAltitudeOnEightyPercentOfTheSamples() {
        var ok = drive(100)
        for i in stride(from: 0, to: 100, by: 5) { ok[i].alt = nil }
        XCTAssertNotNil(RouteMetrics.elevationSeries(ok, cum: RouteMetrics.cumulativeMiles(ok)), "80%")
        var less = drive(100)
        for i in stride(from: 0, to: 100, by: 4) { less[i].alt = nil }
        for i in stride(from: 1, to: 100, by: 4) { less[i].alt = nil }
        XCTAssertNil(RouteMetrics.elevationSeries(less, cum: RouteMetrics.cumulativeMiles(less)))
    }

    func testAFlatRoadHasNoElevationMode() {
        XCTAssertNil(RouteMetrics.series(drive(120, alt: { _ in 100 }), packKWh: 135)[.elevation])
    }

    func testThePackSizeComesFromTheDrivesOwnFigures() throws {
        XCTAssertEqual(try XCTUnwrap(RouteMetrics.packKWh(energyUsedKWh: 27, startSoc: 80, endSoc: 60)), 135, accuracy: 0.001)
        XCTAssertNil(RouteMetrics.packKWh(energyUsedKWh: 27, startSoc: 80, endSoc: 77), "under 5 points is too coarse")
        XCTAssertNil(RouteMetrics.packKWh(energyUsedKWh: nil, startSoc: 80, endSoc: 60))
    }

    func testTheLegendNamesTheModeAndShareTheUnit() {
        XCTAssertEqual(GradientLegend(lo: 2.4, hi: 3.6, metric: .efficiency).line, "Efficiency (est.) 2.4–3.6 mi/kWh")
        XCTAssertEqual(GradientLegend(lo: 100, hi: 450, metric: .elevation).line, "Elevation 328–1,476 ft")
    }

    func testADrivesModesAreTheOnesItsRunsAllCarry() {
        let c = (0..<4).map { RouteSnap.Coord(lat: 40, lon: -100 + Double($0) * 0.001) }
        let with = RouteSnap.Run(coords: c, speeds: [10, 30, 50, 70], extras: [.efficiency: [2, 3, 4, 5]])
        let without = RouteSnap.Run(coords: c, speeds: [10, 30, 50, 70])
        func drawing(_ runs: [RouteSnap.Run]) -> RouteSnap.Drawing {
            RouteSnap.Drawing(pieces: [], routedGaps: [], gaps: [], start: nil, end: nil, hasSpeed: true, runs: runs)
        }
        XCTAssertEqual(drawing([with]).availableMetrics, [.speed, .efficiency])
        XCTAssertEqual(drawing([with, without]).availableMetrics, [.speed], "a run with no value would be a gap in the colour")
    }
}
