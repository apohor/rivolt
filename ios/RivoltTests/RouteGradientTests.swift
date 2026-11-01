import XCTest
@testable import Rivolt

/// The drive map's gradient maths (RouteGradient), with the cases of the web's `metrics.test.ts` so the two colour a drive alike. Plainly made-up
/// numbers; no real drive.
final class RouteGradientTests: XCTestCase {
    private typealias G = RouteGradient

    // MARK: range

    func testRangeIsTheFifthToNinetyFifthPercentileNotMinToMax() throws {
        var v = (0..<100).map { 50 + Double($0 % 10) }
        v[7] = 400 // one spike
        let r = try XCTUnwrap(G.range(values: v))
        XCTAssertLessThan(r.hi, 70)
        XCTAssertGreaterThanOrEqual(r.lo, 50)
    }

    func testRangeInterpolatesLinearly() throws {
        // 0...10 in eleven steps: the 5th percentile is index 0.5, the 95th is 9.5.
        let r = try XCTUnwrap(G.range(values: (0...10).map(Double.init)))
        XCTAssertEqual(r.lo, 0.5, accuracy: 1e-9)
        XCTAssertEqual(r.hi, 9.5, accuracy: 1e-9)
    }

    func testRangeIgnoresValuesThatAreNotFinite() throws {
        let r = try XCTUnwrap(G.range(values: [.nan, 10, 20, .infinity, 30, -.infinity, 40, 50]))
        XCTAssertGreaterThanOrEqual(r.lo, 10)
        XCTAssertLessThanOrEqual(r.hi, 50)
    }

    func testAFlatOrEmptyRangeIsNilNotAZeroWidthRange() {
        XCTAssertNil(G.range(values: [5, 5, 5]))
        XCTAssertNil(G.range(values: [7]))
        XCTAssertNil(G.range(values: []))
        XCTAssertNil(G.range(values: [.nan, .nan]))
    }

    func testSpeedHasNoMinimumSpread() {
        XCTAssertNotNil(G.range(values: [30, 31, 32, 33, 34], metric: .speed))
    }

    /// The rule that hides a mode: efficiency needs a spread of 5% of its mean, elevation of 10 m.
    func testEfficiencyNeedsFivePercentOfItsMean() {
        let steady = (0..<100).map { 3.0 + Double($0 % 2) * 0.05 } // spread 0.05 on a mean of 3.025: 1.7%
        XCTAssertNil(G.range(values: steady, metric: .efficiency))
        let varying = (0..<100).map { 3.0 + Double($0 % 10) * 0.1 } // spread near 0.85 on a mean near 3.45
        XCTAssertNotNil(G.range(values: varying, metric: .efficiency))
    }

    func testElevationNeedsTenMetres() {
        let nearlyFlat = (0..<100).map { 100 + Double($0 % 2) * 3 } // 3 m
        XCTAssertNil(G.range(values: nearlyFlat, metric: .elevation))
        let climb = (0..<100).map { 100 + Double($0) * 2 } // 200 m
        XCTAssertNotNil(G.range(values: climb, metric: .elevation))
    }

    // MARK: stops

    func testStopsRunStrictlyIncreasingFromZeroToOneAndClampTIntoTheRamp() throws {
        let progress = [0.0, 0.25, 0.5, 0.75, 1]
        let values = [10.0, 20, 30, 40, 1000] // the last is far above hi
        let s = G.stops(progress: progress, values: values, lo: 10, hi: 40, count: 20)
        XCTAssertEqual(s.count, 21)
        XCTAssertEqual(s.first?.offset, 0)
        XCTAssertEqual(s.last?.offset, 1)
        for (a, b) in zip(s, s.dropFirst()) { XCTAssertGreaterThan(b.offset, a.offset) }
        XCTAssertEqual(s.first?.t, 0)
        XCTAssertEqual(s.last?.t, 1) // 1000 against 10...40 is the top of the ramp, not past it
        for stop in s { XCTAssertTrue((0...1).contains(stop.t), "t = \(stop.t)") }
    }

    func testStopsInterpolateBetweenVerticesByProgress() {
        // Values 0 at progress 0 and 10 at progress 1: halfway along the line is halfway up the ramp.
        let s = G.stops(progress: [0, 1], values: [0, 10], lo: 0, hi: 10, count: 10)
        for stop in s { XCTAssertEqual(stop.t, stop.offset, accuracy: 1e-9) }
    }

    func testAZeroLengthLineIsTwoStopsOfOneColour() {
        for progress in [[0.0, 0.0], [0.5, 0.5], []] as [[Double]] {
            let s = G.stops(progress: progress, values: [5, 5], lo: 0, hi: 10)
            XCTAssertEqual(s.count, 2)
            XCTAssertEqual(s.map(\.offset), [0, 1])
            XCTAssertEqual(s.map(\.t), [0.5, 0.5])
        }
        XCTAssertEqual(G.stops(progress: [0, 1], values: [], lo: 0, hi: 10).count, 2)
    }

    func testAFlatRampIsOneColour() {
        let s = G.stops(progress: [0, 1], values: [5, 5], lo: 5, hi: 5, count: 4)
        XCTAssertEqual(s.count, 5)
        XCTAssertTrue(s.allSatisfy { $0.t == 0.5 })
    }

    func testRepeatedProgressMakesNoRepeatedOffsetsAndNoDivisionByZero() {
        // A stopped truck: three vertices at the same place (the same progress), then it moves.
        let progress = [0.0, 0.5, 0.5, 0.5, 1]
        let values = [10.0, 20, 20, 20, 30]
        let s = G.stops(progress: progress, values: values, lo: 10, hi: 30, count: 30)
        XCTAssertEqual(s.count, 31)
        for (a, b) in zip(s, s.dropFirst()) { XCTAssertGreaterThan(b.offset, a.offset) }
        for stop in s { XCTAssertTrue(stop.t.isFinite && (0...1).contains(stop.t)) }
    }

    func testAValueThatIsNotFiniteCountsAsLo() {
        let s = G.stops(progress: [0, 0.5, 1], values: [20, .nan, 20], lo: 10, hi: 30, count: 2)
        // The middle offset sits on the NaN vertex: lo, so t = 0. Its neighbours interpolate toward the finite one.
        XCTAssertEqual(s[1].t, 0.5, accuracy: 1e-9) // exactly on vertex 1 (progress 0.5): its own value is NaN, the one before it is 20
        XCTAssertTrue(s.allSatisfy { $0.t.isFinite })
    }

    func testProgressThatDoesNotStartAtZeroIsScaledToTheLine() {
        // A line whose progress runs 0.2 to 0.8 still gets offsets 0...1.
        let s = G.stops(progress: [0.2, 0.8], values: [0, 10], lo: 0, hi: 10, count: 5)
        XCTAssertEqual(s.map(\.offset), [0, 0.2, 0.4, 0.6, 0.8, 1])
        XCTAssertEqual(s.last?.t ?? -1, 1, accuracy: 1e-9)
    }

    /// The web's "a highway drive reads teal to yellow, not red": speeds all in the 60s, scaled to their own percentiles, span the whole ramp.
    func testAHighwayDriveSpansTheWholeRampOnItsOwnPercentiles() throws {
        let progress = (0..<50).map { Double($0) / 49 }
        let values = (0..<50).map { 60 + Double($0 % 10) }
        let r = try XCTUnwrap(G.range(values: values))
        let s = G.stops(progress: progress, values: values, lo: r.lo, hi: r.hi)
        XCTAssertLessThan(s.map(\.t).min() ?? 1, 0.2)
        XCTAssertGreaterThan(s.map(\.t).max() ?? 0, 0.8)
    }

    // MARK: legend

    func testLegendSaysTheEndsInTheAccountsUnits() {
        XCTAssertEqual(G.legend(lo: 32, hi: 55, metric: .speed).min, "32 mph")
        XCTAssertEqual(G.legend(lo: 32, hi: 55, metric: .speed).max, "55 mph")
        // 32 mph and 55 mph in km/h: 51.5 and 88.5 round to 51 and 89 (the web rounds the same way).
        let km = G.legend(lo: 32, hi: 55, metric: .speed, unit: .kilometres)
        XCTAssertEqual(km.min, "51 km/h")
        XCTAssertEqual(km.max, "89 km/h")
    }

    func testEfficiencyIsOneDecimalPerKWh() {
        let mi = G.legend(lo: 3.44, hi: 5.06, metric: .efficiency)
        XCTAssertEqual(mi.min, "3.4 mi/kWh")
        XCTAssertEqual(mi.max, "5.1 mi/kWh")
        // mi/kWh to km/kWh divides by 0.6213711922: 3.1 mi/kWh is 5.0 km/kWh.
        XCTAssertEqual(G.legend(lo: 3.1, hi: 4.0, metric: .efficiency, unit: .kilometres).min, "5.0 km/kWh")
    }

    func testElevationIsFeetOrMetresWithThousandsSeparators() {
        XCTAssertEqual(G.legend(lo: 100, hi: 500, metric: .elevation).min, "328 ft")
        XCTAssertEqual(G.legend(lo: 100, hi: 500, metric: .elevation).max, "1,640 ft")
        XCTAssertEqual(G.legend(lo: 100, hi: 2500, metric: .elevation, unit: .kilometres).max, "2,500 m")
        XCTAssertEqual(G.legend(lo: 100, hi: 1_500_000, metric: .elevation, unit: .kilometres).max, "1,500,000 m")
    }

    func testBelowSeaLevelKeepsItsSign() {
        // -10 m is -32.8 ft, which rounds to -33; a half rounds up (toward +infinity) as the web's Math.round does: -2.5 is -2.
        XCTAssertEqual(G.legend(lo: -10, hi: 5, metric: .elevation).min, "-33 ft")
        XCTAssertEqual(G.format(-2.5, metric: .elevation, unit: .kilometres), "-2 m")
        XCTAssertEqual(G.format(2.5, metric: .elevation, unit: .kilometres), "3 m")
    }

    func testAValueThatIsNotFiniteIsADash() {
        XCTAssertEqual(G.format(.nan, metric: .speed, unit: .miles), "—")
        XCTAssertEqual(G.format(.infinity, metric: .elevation, unit: .miles), "—")
    }
}
