import XCTest
@testable import Rivolt

final class PinPillsTests: XCTestCase {
    private let size = CGSize(width: 343, height: 220)

    func testThePillGoesOnTheSideWithMoreRoom() {
        let left = PinPills.place(pin: CGPoint(x: 60, y: 100), width: 90, in: size, avoiding: [])
        XCTAssertEqual(left.side, .right, "a pin near the left edge puts its pill on the right")
        XCTAssertEqual(left.anchor.x, 60 + PinPills.gap, accuracy: 0.001)
        let right = PinPills.place(pin: CGPoint(x: 300, y: 100), width: 90, in: size, avoiding: [])
        XCTAssertEqual(right.side, .left)
        XCTAssertEqual(right.anchor.x, 300 - PinPills.gap, accuracy: 0.001, "the pill grows away from its pin")
    }

    func testThePillStaysInsideTheMapAndClearOfTheCorners() {
        let keep = PinPills.keepClear(size: size, legend: true)
        // A pin at the bottom left: right of it sits on the legend, so the pill goes above.
        let s = PinPills.place(pin: CGPoint(x: 30, y: 200), width: 90, in: size, avoiding: keep)
        let bounds = CGRect(origin: .zero, size: size).insetBy(dx: PinPills.margin, dy: PinPills.margin)
        XCTAssertTrue(bounds.contains(s.rect), "\(s.rect)")
        XCTAssertFalse(keep.contains { $0.intersects(s.rect) }, "the pill must not sit on the legend or the credit")
    }

    func testTwoPillsNeverOverlap() {
        let items = PinPillsOverlay.items(size: size, start: CGPoint(x: 150, y: 100), end: CGPoint(x: 230, y: 110), startTime: "9:15 PM", endTime: "11:28 PM", legend: true)
        XCTAssertEqual(items.count, 2)
        XCTAssertFalse(items[0].spot.rect.intersects(items[1].spot.rect))
    }

    func testARoundTripGetsOnePill() {
        let items = PinPillsOverlay.items(size: size, start: CGPoint(x: 150, y: 100), end: CGPoint(x: 155, y: 104), startTime: "9:15 PM", endTime: "11:28 PM", legend: false)
        XCTAssertEqual(items.map(\.text), ["Start 9:15 PM · End 11:28 PM"])
    }

    func testTheWordsAreStartAndEndWithTheTime() {
        let t = PinPills.text(start: "9:15 PM", end: "11:28 PM", shared: false)
        XCTAssertEqual(t.start, "Start 9:15 PM")
        XCTAssertEqual(t.end, "End 11:28 PM")
        XCTAssertNil(t.both)
        XCTAssertEqual(PinPills.text(start: nil, end: "11:28 PM", shared: true).end, "End 11:28 PM", "a missing time is no pill, not a broken one")
    }

    func testATinyMapStillGetsAPillInsideIt() {
        let tiny = CGSize(width: 120, height: 60)
        let s = PinPills.place(pin: CGPoint(x: 60, y: 30), width: 140, in: tiny, avoiding: PinPills.keepClear(size: tiny, legend: true))
        XCTAssertGreaterThanOrEqual(s.rect.minX, PinPills.margin - 0.001)
    }
}
