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
        // A pin near the bottom left: right of it sits on the legend, so the pill goes above (nudged in from the edge).
        let s = PinPills.place(pin: CGPoint(x: 30, y: 175), width: 90, in: size, avoiding: keep)
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
        let s = PinPills.place(pin: CGPoint(x: 60, y: 30), width: 80, in: tiny, avoiding: PinPills.keepClear(size: tiny, legend: true))
        XCTAssertGreaterThanOrEqual(s.rect.minX, PinPills.margin - 0.001)
        XCTAssertLessThanOrEqual(s.rect.maxX, tiny.width - PinPills.margin + 0.001)
        // Wider than the map itself: pinned to the left margin, never off the left edge.
        let wide = PinPills.place(pin: CGPoint(x: 60, y: 30), width: 140, in: tiny, avoiding: [])
        XCTAssertGreaterThanOrEqual(wide.rect.minX, PinPills.margin - 0.001)
    }

    func testAPillNeverLiesAlongTheLine() {
        // The line leaves the pin to the right; the roomier side is the right, so the pill must go elsewhere.
        let pin = CGPoint(x: 60, y: 100)
        let line = [pin, CGPoint(x: 110, y: 98), CGPoint(x: 160, y: 96)]
        let s = PinPills.place(pin: pin, width: 90, in: size, avoiding: [], line: line)
        XCTAssertNotEqual(s.side, .right)
        XCTAssertFalse(PinPills.touches(line, s.rect))
        XCTAssertTrue(PinPills.touches(line, CGRect(x: 70, y: 88, width: 90, height: 22)))
    }

    func testARealWidthReplacesTheEstimate() {
        let wide = PinPillsOverlay.items(size: size, start: CGPoint(x: 40, y: 100), end: nil, startTime: "9:15 PM", endTime: nil, legend: false, widths: [1: 300])
        let est = PinPillsOverlay.items(size: size, start: CGPoint(x: 40, y: 100), end: nil, startTime: "9:15 PM", endTime: nil, legend: false)
        XCTAssertEqual(wide.first?.spot.rect.width, 300)
        XCTAssertLessThan(est.first?.spot.rect.width ?? 999, 300)
    }

    // The owner's bug: a Start pin right beside the legend card (with its mode switch), the pill drawn on top of it.
    func testAPinBesideAMeasuredLegendNeverGetsAPillOnIt() {
        let legend = CGRect(x: 0, y: 140, width: 215, height: 80).insetBy(dx: -4, dy: -4)
        let credit = CGRect(x: 230, y: 190, width: 113, height: 30).insetBy(dx: -4, dy: -4)
        for pin in [CGPoint(x: 205, y: 190), CGPoint(x: 120, y: 150), CGPoint(x: 225, y: 175), CGPoint(x: 260, y: 160)] {
            let items = PinPillsOverlay.items(size: size, start: pin, end: nil, startTime: "5:54 PM", endTime: nil, legend: true, obstacles: [legend, credit])
            XCTAssertEqual(items.count, 1)
            XCTAssertFalse(items[0].spot.rect.intersects(legend), "\(pin): \(items[0].spot.rect) is on the legend")
            XCTAssertFalse(items[0].spot.rect.intersects(credit), "\(pin): \(items[0].spot.rect) is on the credit")
        }
    }

    // A pin with no free side for the full pill gets the short one ("Start") where that fits, and it still says the time aloud.
    func testWhereTheTimeHasNoRoomThePillIsJustTheName() {
        let tight = CGSize(width: 150, height: 60)
        let wall = [CGRect(x: 0, y: 0, width: 150, height: 17), CGRect(x: 0, y: 43, width: 150, height: 17)]
        let items = PinPillsOverlay.items(size: tight, start: CGPoint(x: 30, y: 30), end: nil, startTime: "5:54 PM", endTime: nil, legend: false, obstacles: wall)
        XCTAssertEqual(items.map(\.text), ["Start"])
        XCTAssertEqual(items.map(\.spoken), ["Start 5:54 PM"])
        XCTAssertEqual(PinPills.shortText("Start 9:15 PM · End 11:28 PM"), "Start · End")
        XCTAssertEqual(PinPills.parts(of: "Start"), [PinPills.Part(name: "Start", time: "")])
    }

    func testWithRoomItKeepsTheTime() {
        let items = PinPillsOverlay.items(size: size, start: CGPoint(x: 60, y: 60), end: nil, startTime: "5:54 PM", endTime: nil, legend: false, obstacles: [])
        XCTAssertEqual(items.map(\.text), ["Start 5:54 PM"])
    }
}
