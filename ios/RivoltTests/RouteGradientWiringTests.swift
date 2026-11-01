import SwiftUI
import XCTest
@testable import Rivolt

final class RouteGradientWiringTests: XCTestCase {
    private func coords(_ n: Int) -> [RouteSnap.Coord] { (0..<n).map { RouteSnap.Coord(lat: 45 + Double($0) * 0.001, lon: -122) } }

    func testARunCarriesItsSpeedsAndProgressAlongTheLine() {
        let r = RouteSnap.Run(coords: coords(4), speeds: [10, nil, 30, 40])
        XCTAssertEqual(r.progress.first, 0)
        XCTAssertEqual(r.progress.last ?? 0, 1, accuracy: 1e-9)
        XCTAssertEqual(r.progress.count, 4)
        XCTAssertEqual(r.filledSpeeds, [10, 10, 30, 40], "a gap carries the last known speed")
        XCTAssertEqual(RouteSnap.Run(coords: coords(2), speeds: [nil, 20]).filledSpeeds, [20, 20], "leading gaps take the first known")
        XCTAssertEqual(RouteSnap.Run(coords: coords(2), speeds: [nil, nil]).filledSpeeds, [])
    }

    func testTheGradientNeedsSpeedsThatVary() {
        func drawing(_ speeds: [Double?]) -> RouteSnap.Drawing {
            RouteSnap.Drawing(pieces: [], routedGaps: [], gaps: [], start: nil, end: nil, hasSpeed: true, runs: RouteSnap.runs(coords(speeds.count), speeds: speeds))
        }
        XCTAssertTrue(drawing([20, 35, 50, 62, 70, 30, 41, 55, 28, 66]).usesGradient)
        XCTAssertFalse(drawing([40, 40, 40, 40]).usesGradient, "a flat drive keeps the buckets")
        XCTAssertFalse(drawing([nil, nil, nil]).usesGradient, "no speeds, no gradient")
        let range = drawing([0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100]).speedRange
        XCTAssertEqual(range?.lo ?? -1, 5, accuracy: 1e-9, "the 5th percentile, not the minimum")
        XCTAssertEqual(range?.hi ?? -1, 95, accuracy: 1e-9)
    }

    func testTooManyRunsKeepTheBuckets() {
        let many = (0...RouteSnap.maxGradientRuns).map { _ in RouteSnap.Run(coords: coords(3), speeds: [10, 40, 80]) }
        let d = RouteSnap.Drawing(pieces: [], routedGaps: [], gaps: [], start: nil, end: nil, hasSpeed: true, runs: many)
        XCTAssertFalse(d.usesGradient)
    }

    func testARampRunsFromLowToHighAndClamps() {
        let traits = UITraitCollection(userInterfaceStyle: .dark)
        let lo = RouteRamp.speed.color(at: 0, traits: traits), hi = RouteRamp.speed.color(at: 1, traits: traits)
        XCTAssertNotEqual(lo, hi)
        XCTAssertEqual(RouteRamp.speed.color(at: -3, traits: traits), lo)
        XCTAssertEqual(RouteRamp.speed.color(at: 9, traits: traits), hi)
        XCTAssertEqual(RouteRamp.speed.color(at: .nan, traits: traits), lo)
    }

    func testTheLegendReadsTheDrivesOwnScaleInTheAccountsUnits() {
        let l = RouteGradient.legend(lo: 32, hi: 55, metric: .speed, unit: .miles)
        XCTAssertEqual(l.min, "32 mph")
        XCTAssertEqual(l.max, "55 mph")
        XCTAssertEqual(GradientLegend.title, "Speed")
    }
}
