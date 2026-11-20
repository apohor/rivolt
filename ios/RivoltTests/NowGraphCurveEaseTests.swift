import CoreGraphics
import XCTest
@testable import Rivolt

final class NowGraphCurveEaseTests: XCTestCase {
    func testAGentleLineIsLeftAlone() {
        let pts = [CGPoint(x: 0, y: 10), CGPoint(x: 40, y: 12), CGPoint(x: 80, y: 13)]
        let eased = NowGraphCurve.easeSteps(pts)
        XCTAssertEqual(eased.points, pts)
        XCTAssertTrue(eased.hold.isEmpty)
    }

    func testAJumpBecomesALevelEndedRampThatEndsOnTheSecondReading() {
        let eased = NowGraphCurve.easeSteps([CGPoint(x: 0, y: 10), CGPoint(x: 100, y: 10), CGPoint(x: 103, y: 40), CGPoint(x: 200, y: 41)])
        XCTAssertEqual(eased.points.count, 4)
        XCTAssertEqual(eased.points[2], CGPoint(x: 103, y: 40))
        XCTAssertEqual(eased.hold, [1, 2])
        XCTAssertEqual(eased.points[1].y, 10)
        XCTAssertGreaterThanOrEqual(eased.points[2].x - eased.points[1].x, 12)
    }

    func testARealJumpAfterALongPlateauKeepsAShortRun() {
        let flat = (0..<10).map { CGPoint(x: CGFloat($0) * 10, y: 50) }
        let eased = NowGraphCurve.easeSteps(flat + [CGPoint(x: 100, y: 62), CGPoint(x: 110, y: 62)])
        let start = eased.points.last(where: { $0.y == 50 && $0.x <= 100 })!
        XCTAssertLessThanOrEqual(100 - start.x, 28)
    }

    func testHeldPointsLeaveAndArriveLevel() {
        let segs = NowGraphCurve.segments([CGPoint(x: 0, y: 0), CGPoint(x: 20, y: 30)], hold: [0, 1])
        XCTAssertEqual(segs.count, 1)
        XCTAssertEqual(segs[0].c1.y, 0, accuracy: 0.001)
        XCTAssertEqual(segs[0].c2.y, 30, accuracy: 0.001)
    }

    func testTwoJumpsInARowAreBothEased() {
        let pts = [(0.0, 0.0), (100, 0), (103, 30), (160, 30), (163, 60), (220, 60)].map { CGPoint(x: $0.0, y: $0.1) }
        let eased = NowGraphCurve.easeSteps(pts)
        let into = eased.points.firstIndex { $0.x == 163 }!
        XCTAssertTrue(eased.hold.contains(into))
        XCTAssertTrue(eased.hold.contains(into - 1))
        XCTAssertGreaterThanOrEqual(eased.points[into].x - eased.points[into - 1].x, 12)
    }

    func testAPinnedReadingStaysOnTheLine() {
        let pts = [(0.0, 50.0), (60, 50), (100, 50), (103, 80), (200, 81)].map { CGPoint(x: $0.0, y: $0.1) }
        let eased = NowGraphCurve.easeSteps(pts, pinned: [2])
        XCTAssertTrue(eased.points.contains(CGPoint(x: 100, y: 50)), "the wake dot's reading is still a point of the path")
        XCTAssertTrue(eased.hold.contains(eased.points.firstIndex(of: CGPoint(x: 100, y: 50))!))
        let xs = eased.points.map(\.x)
        XCTAssertEqual(xs, xs.sorted())
    }

    func testNeverReordersTheReadings() {
        let pts = [(0.0, 0.0), (5, 0), (7, 30), (9, 31), (11, 70), (40, 71)].map { CGPoint(x: $0.0, y: $0.1) }
        let xs = NowGraphCurve.easeSteps(pts).points.map(\.x)
        XCTAssertEqual(xs, xs.sorted())
    }
}
