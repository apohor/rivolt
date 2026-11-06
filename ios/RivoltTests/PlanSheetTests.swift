import SwiftUI
import XCTest
@testable import Rivolt

@MainActor
final class PlanSheetTests: XCTestCase {
    typealias Sheet = DetentSheet<EmptyView, EmptyView>

    func testTheThreeHeightsGrowAndStayInsideTheScreen() {
        let h = 700.0
        let heights = PlanSheetDetent.allCases.map { Sheet.height($0, in: h) }
        XCTAssertEqual(heights, heights.sorted())
        XCTAssertEqual(heights.count, 3)
        XCTAssertLessThan(heights[2], h)
        XCTAssertEqual(Sheet.height(.half, in: h), h * 0.46, accuracy: 0.001)
    }

    func testADragSettlesOnTheNearestHeightAndAFlickCarriesFurther() {
        let h = 700.0
        // A short drag up from half stays at half; a long one reaches full; a long one down peeks.
        XCTAssertEqual(Sheet.settle(from: .half, raised: 20, velocity: 0, container: h), .half)
        XCTAssertEqual(Sheet.settle(from: .half, raised: 300, velocity: 0, container: h), .full)
        XCTAssertEqual(Sheet.settle(from: .half, raised: -250, velocity: 0, container: h), .peek)
        // A flick from the peek with little travel still opens it.
        XCTAssertNotEqual(Sheet.settle(from: .peek, raised: 40, velocity: 1500, container: h), .peek)
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
                        DetentSheet(detent: .constant(d), container: size.height, scrolls: false) {
                            VStack(alignment: .leading, spacing: 6) {
                                HStack(alignment: .firstTextBaseline) {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text("Seattle → Spokane").themeText(.title, color: Theme.textPrimary)
                                        Text("270 mi · 4 h 5 min driving · 25 min charging").themeText(.subline, color: Theme.textSecondary)
                                    }
                                    Spacer(minLength: 8)
                                    Text("Edit").themeText(.value, color: Theme.accent).frame(minWidth: 44, minHeight: 44)
                                }
                                HStack(spacing: 8) {
                                    ForEach(["Whole trip", "Day 1", "Day 2"], id: \.self) { t in
                                        Text(t).themeText(.subline, color: t == "Day 1" ? Theme.textPrimary : Theme.textSecondary).padding(.horizontal, 12).frame(minHeight: 36)
                                            .background(t == "Day 1" ? Theme.borderStrong : Theme.background, in: Capsule())
                                    }
                                }
                            }
                        } content: {
                            VStack(alignment: .leading, spacing: 10) {
                                DrivingPerDayRow(model: TripPlanModel())
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
