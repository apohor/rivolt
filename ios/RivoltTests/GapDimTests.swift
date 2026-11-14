import XCTest
@testable import Rivolt

/// gap_dim: a dropout takes the colour the route has where it begins.
final class GapDimTests: XCTestCase {
    private func coords(_ lat0: Double) -> [RouteSnap.Coord] {
        (0..<4).map { RouteSnap.Coord(lat: lat0 + Double($0) * 0.001, lon: -100) }
    }

    func testABucketedDropoutTakesTheNearestPiecesColour() {
        let slow = RouteSnap.Piece(coords: coords(40), speed: .under5), fast = RouteSnap.Piece(coords: coords(40.1), speed: .over65)
        let d = RouteSnap.Drawing(pieces: [slow, fast], routedGaps: [],
                                  gaps: [RouteSnap.Gap(from: slow.coords[3], to: fast.coords[0])], start: nil, end: nil, hasSpeed: true)
        let c = d.gapColors(metric: .speed, gradientOn: false, traits: UITraitCollection())
        XCTAssertEqual(c.count, 1)
        XCTAssertEqual(c[0], UIColor(RouteMapView.color(.under5)))
    }

    func testAGradientDropoutTakesTheRampColourOfTheRunEndBeforeIt() throws {
        let lo = RouteSnap.Run(coords: coords(40), speeds: [10, 10, 10, 10]), hi = RouteSnap.Run(coords: coords(40.1), speeds: [70, 70, 70, 70])
        let d = RouteSnap.Drawing(pieces: [], routedGaps: [], gaps: [RouteSnap.Gap(from: lo.coords[3], to: hi.coords[0])],
                                  start: nil, end: nil, hasSpeed: true, runs: [lo, hi])
        let range = try XCTUnwrap(d.range(for: .speed))
        let traits = UITraitCollection()
        let c = d.gapColors(metric: .speed, gradientOn: true, traits: traits)
        XCTAssertEqual(c, [RouteRamp.ramp(.speed).color(at: range.hi > range.lo ? 0 : 0.5, traits: traits)])
    }

    func testNoGapsNoColours() {
        let d = RouteSnap.Drawing(pieces: [], routedGaps: [], gaps: [], start: nil, end: nil, hasSpeed: false)
        XCTAssertTrue(d.gapColors(metric: .speed, gradientOn: false, traits: UITraitCollection()).isEmpty)
    }

    func testDimOpacityMatchesTheWeb() {
        XCTAssertEqual(RouteSnap.Drawing.gapDimOpacity, 0.35)
    }
}
