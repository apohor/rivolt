import XCTest
@testable import Rivolt

final class MapPolishTests: XCTestCase {
    func testTheLegendIsOneLineWithTheUnitOnce() {
        XCTAssertEqual(GradientLegend(lo: 12, hi: 64).line, "Speed 12–64 mph")
    }

    func testAPillsPartsAreItsNamesAndTimes() {
        XCTAssertEqual(PinPills.parts(of: "Start 9:15 PM"), [PinPills.Part(name: "Start", time: "9:15 PM")])
        XCTAssertEqual(PinPills.parts(of: "Start 9:15 PM · End 11:28 PM"),
                       [PinPills.Part(name: "Start", time: "9:15 PM"), PinPills.Part(name: "End", time: "11:28 PM")])
    }

    func testAFilledStretchCarriesTheSpeedAcrossFromItsNeighbours() throws {
        let coords = (0...4).map { RouteSnap.Coord(lat: 40, lon: -100 + Double($0) * 0.001) }
        let run = try XCTUnwrap(RouteSnap.filledRun(coords, from: 20, to: 60))
        let s = run.speeds.compactMap { $0 }
        XCTAssertEqual(s.count, 5)
        XCTAssertEqual(s.first ?? 0, 20, accuracy: 0.001)
        XCTAssertEqual(s.last ?? 0, 60, accuracy: 0.001)
        XCTAssertEqual(s[2], 40, accuracy: 0.5, "evenly spaced: the middle is the middle")
        XCTAssertEqual(s, s.sorted(), "never goes the wrong way")
    }

    func testAFilledStretchUsesTheOneNeighbourItHasAndNothingWithNone() throws {
        let coords = [RouteSnap.Coord(lat: 40, lon: -100), RouteSnap.Coord(lat: 40, lon: -99.99)]
        XCTAssertEqual(try XCTUnwrap(RouteSnap.filledRun(coords, from: nil, to: 33)).speeds.compactMap { $0 }, [33, 33])
        XCTAssertNil(RouteSnap.filledRun(coords, from: nil, to: nil))
    }
}
