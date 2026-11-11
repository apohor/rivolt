import SwiftUI
import XCTest
@testable import Rivolt

@MainActor
final class PlanSheetTests: XCTestCase {
    typealias Sheet = SheetLayout

    func testTheThreeHeightsGrowAndStayInsideTheScreen() {
        let h = 700.0
        let heights = PlanSheetDetent.allCases.map { Sheet.height($0, in: h) }
        XCTAssertEqual(heights, heights.sorted())
        XCTAssertEqual(heights.count, 3)
        XCTAssertLessThan(heights[2], h)
        XCTAssertEqual(Sheet.height(.half, in: h), h * 0.46, accuracy: 0.001)
    }

    func testThePeekIsMeasuredAndCappedAtAThirdOfTheMap() {
        let h = 700.0
        XCTAssertEqual(Sheet.height(.peek, in: h, peekNeed: 120), 120, "the title and one summary line, as measured")
        XCTAssertEqual(Sheet.height(.peek, in: h, peekNeed: 400), h * 0.35, accuracy: 0.001, "never more than 35 % of the map")
        XCTAssertGreaterThan(Sheet.height(.half, in: 300, peekNeed: 400), Sheet.height(.peek, in: 300, peekNeed: 400), "half stays above the peek")
    }

    func testAMediumFlickFromThePeekStopsAtHalf() {
        let h = 700.0
        // 40 pt up at 1.5 pt/ms: one step, not two.
        XCTAssertEqual(Sheet.settle(from: .peek, raised: 40, velocity: 1500, container: h), .half)
        XCTAssertEqual(Sheet.settle(from: .half, raised: 40, velocity: 1500, container: h), .full)
        XCTAssertEqual(Sheet.settle(from: .full, raised: -40, velocity: -1500, container: h), .half)
        // A slow, short drag goes back where it was.
        XCTAssertEqual(Sheet.settle(from: .half, raised: 20, velocity: 0, container: h), .half)
        XCTAssertEqual(Sheet.settle(from: .half, raised: -20, velocity: -100, container: h), .half)
    }

    func testADragPastHalfGoesOnToFull() {
        let h = 700.0
        let peek = Sheet.height(.peek, in: h), half = Sheet.height(.half, in: h), full = Sheet.height(.full, in: h)
        // Past half and a third of the way on: full, even when let go slowly.
        XCTAssertEqual(Sheet.settle(from: .peek, raised: half - peek + (full - half) * 0.4, velocity: 0, container: h), .full)
        // Just past half: half.
        XCTAssertEqual(Sheet.settle(from: .peek, raised: half - peek + 10, velocity: 0, container: h), .half)
        // A third of the way from peek to half: half.
        XCTAssertEqual(Sheet.settle(from: .peek, raised: (half - peek) * 0.4, velocity: 0, container: h), .half)
        // All the way down from full: the peek.
        XCTAssertEqual(Sheet.settle(from: .full, raised: -(full - peek), velocity: 0, container: h), .peek)
    }

    func testFullyOpenADragDownMovesTheSheetOnlyWithTheContentAtItsTop() {
        XCTAssertTrue(Sheet.dragMovesSheet(contentScrolls: true, contentAtTop: true, raised: -30), "at the top: the sheet closes")
        XCTAssertFalse(Sheet.dragMovesSheet(contentScrolls: true, contentAtTop: false, raised: -30), "scrolled: the content scrolls back")
        XCTAssertFalse(Sheet.dragMovesSheet(contentScrolls: true, contentAtTop: true, raised: 30), "up: the content scrolls on")
        XCTAssertTrue(Sheet.dragMovesSheet(contentScrolls: false, contentAtTop: false, raised: 30), "below full a drag always moves the sheet")
    }

    func testOnlyAnUprightDragMovesTheSheet() {
        XCTAssertTrue(Sheet.isUpright(dx: 3, dy: -12))
        XCTAssertFalse(Sheet.isUpright(dx: 14, dy: 6), "a sideways swipe on the day chips")
    }

    func testARotationKeepsTheDetentAndRecomputesItsHeight() {
        // The detent is the owner's state; only the height is a function of the container.
        for d in PlanSheetDetent.allCases {
            XCTAssertNotEqual(Sheet.height(d, in: 700, peekNeed: 110, headerNeed: 200), Sheet.height(d, in: 360, peekNeed: 110, headerNeed: 200) + 1000)
        }
        XCTAssertEqual(Sheet.height(.half, in: 360), 360 * 0.46, accuracy: 0.001)
        XCTAssertEqual(Sheet.height(.full, in: 360), 352)
    }

    func testTheTitleNamesTheTwoPlaces() {
        XCTAssertEqual(PlanSheetLogic.title(origin: "Seattle, Washington, United States", destination: "Spokane, Washington, United States"), "Seattle → Spokane")
        XCTAssertEqual(PlanSheetLogic.title(origin: nil, destination: "Spokane, Washington"), "To Spokane")
        XCTAssertEqual(PlanSheetLogic.title(origin: nil, destination: nil), "To Your trip")
    }

    /// The sheet at each height over a stand-in map, light and dark, at the default and a large text size.
    func testRender() throws {
        Theme.registerFonts()
        let size = CGSize(width: 390, height: 640)
        for scheme in [ColorScheme.light, .dark] {
            for (label, type, details) in [("default", DynamicTypeSize.large, PlanSheetDetent.allCases), ("ax3", .accessibility3, [PlanSheetDetent.half])] {
                for d in details {
                    let page = ZStack(alignment: .bottom) {
                        Theme.card
                        Path { p in p.move(to: CGPoint(x: 70, y: 380)); p.addLine(to: CGPoint(x: 200, y: 200)); p.addLine(to: CGPoint(x: 320, y: 90)) }.stroke(Theme.accent, lineWidth: 4)
                        Circle().fill(Theme.routeStart).frame(width: 14, height: 14).position(x: 70, y: 380)
                        Circle().fill(Theme.chargePin).frame(width: 22, height: 22).overlay(Circle().strokeBorder(Theme.accent, lineWidth: 2).frame(width: 30, height: 30)).position(x: 200, y: 200)
                        Circle().fill(Theme.routeEnd).frame(width: 14, height: 14).position(x: 320, y: 90)
                        DetentSheet(detent: .constant(d), container: size.height, scrolls: false, peekNeed: 118) {
                            PlanSheetHeader(title: "Seattle → Spokane", summary: "270 mi · 4 h 5 min driving · 25 min charging", onEdit: {})
                        } header: {
                            HStack(spacing: 8) {
                                ForEach(["Whole trip", "Day 1", "Day 2"], id: \.self) { t in
                                    Text(t).themeText(.subline, color: t == "Day 1" ? Theme.textPrimary : Theme.textSecondary).padding(.horizontal, 12).frame(minHeight: 36)
                                        .background(t == "Day 1" ? Theme.borderStrong : Theme.background, in: Capsule())
                                }
                            }
                        } above: {
                            PlanSheetEdge(frameUnix: Int(Date().timeIntervalSince1970) - 300)
                        } content: {
                            VStack(alignment: .leading, spacing: 10) {
                                // (The real row is a Stepper, which a snapshot cannot draw.)
                                ThemeSection(title: "Driving per day") { Text("8 h").themeText(.value, color: Theme.textPrimary) }
                                ThemeSection(title: "2 charging stops") {
                                    Text("1 · Electrify America, Ellensburg").themeText(.value, color: Theme.accent)
                                    Text("Charge 25 min · 24% → 80%").themeText(.subline, color: Theme.textSecondary)
                                    Divider().overlay(Theme.border)
                                    Text("2 · Sample Fast Charge, Moses Lake").themeText(.value, color: Theme.textPrimary)
                                }
                            }
                        }
                    }
                    .frame(width: size.width, height: size.height).clipped().background(ThemePageBackground())
                    let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, type))
                    renderer.scale = 2
                    let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "sheet did not render").pngData())
                    let file = "plan-sheet-\(scheme == .dark ? "dark" : "light")-\(label)-\(d == .peek ? "peek" : d == .half ? "half" : "full").png"
                    let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
                    attachment.name = file
                    attachment.lifetime = .keepAlways
                    add(attachment)
                    if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                        try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
                    }
                }
            }
        }
    }
}
