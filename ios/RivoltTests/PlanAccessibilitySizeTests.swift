import SwiftUI
import XCTest
@testable import Rivolt

/// The iPhone planner at the largest text sizes: nothing is clipped or left below the fold where it cannot be reached.
@MainActor
final class PlanAccessibilitySizeTests: XCTestCase {
    private final class Fake: CorridorLayersSource, @unchecked Sendable {
        func corridorLayers(_ body: CorridorLayersRequest) async throws -> CorridorLayers { CorridorLayers() }
    }

    func testChipsWrapOntoRowsAndNoneIsWiderThanTheRoom() {
        XCTAssertEqual(FlowLayout.rows(widths: [100, 100, 100, 100], maxWidth: 393, spacing: 8), [[0, 1, 2], [3]])
        XCTAssertEqual(FlowLayout.rows(widths: [60, 60, 60, 60, 60, 60, 36], maxWidth: 393, spacing: 8), [[0, 1, 2, 3, 4], [5, 6]])
        XCTAssertEqual(FlowLayout.rows(widths: [500, 20], maxWidth: 393, spacing: 8), [[0], [1]], "an item wider than the room still gets a row, and is not lost")
        XCTAssertEqual(FlowLayout.rows(widths: [], maxWidth: 393, spacing: 8), [])
    }

    /// The size a view takes when hosted `width` wide.
    private func size(of view: some View, width: CGFloat) async throws -> CGSize {
        let host = UIHostingController(rootView: view.frame(width: width, alignment: .topLeading))
        // Not the window's top and bottom insets: the size of the view itself.
        host.safeAreaRegions = []
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first, "a window scene")
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: width, height: 900)
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        host.view.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(500))
        return host.sizeThatFits(in: CGSize(width: width, height: 3000))
    }

    func testALabelTooWideForTheRowWrapsItsTextInsteadOfRunningOffTheEdge() async throws {
        let wide = FlowLayout { Text("Preferred only and a good deal more so that it is wider than a phone is").font(.system(size: 30)); Text("x") }
        let s = try await size(of: wide, width: 393)
        XCTAssertLessThanOrEqual(s.width, 393)
        XCTAssertGreaterThan(s.height, 60, "the text took more than one line")
    }

    func testTheLayerChipsWrapAtNormalSizeAndBecomeOneButtonAtLargeText() async throws {
        let model = TripPlanModel()
        model.setPreferred(["ea"])
        await model.setLayer(.chargers, on: true, source: Fake())
        let normal = try await size(of: PlanLayerChips(model: model).environment(AppState()).environment(\.dynamicTypeSize, .large), width: 393)
        XCTAssertLessThanOrEqual(normal.width, 393)
        XCTAssertGreaterThan(normal.height, 36 * 1.5, "at the normal size the chips take two rows, none clipped")
        for type in [DynamicTypeSize.accessibility3, .accessibility5] {
            for width in [CGFloat(393), 375] {
                let s = try await size(of: PlanLayerChips(model: model).environment(AppState()).environment(\.dynamicTypeSize, type), width: width)
                XCTAssertLessThanOrEqual(s.width, width, "\(type), \(width) pt")
                XCTAssertLessThan(s.height, 110, "\(type), \(width) pt: one Layers button on one line, not rows of chips over the map, and not capped below the size asked for")
            }
        }
    }

    func testTheLayerNoteIsAtMostTwoLinesAtTheLargestTextSize() async throws {
        let long = "Showing 304 of 627 · listing from 2026-11-01 · not live · and then a good deal more words so it would run on and on"
        let one = try await size(of: LayerNotePill(text: "Ag").environment(\.dynamicTypeSize, .accessibility5), width: 393).height
        let many = try await size(of: LayerNotePill(text: long).environment(\.dynamicTypeSize, .accessibility5), width: 393).height
        XCTAssertLessThan(many, one * 2.3, "two lines at most, not a third of the map")
    }

    /// The case that slipped: at AX3 and AX5 the map has to stay usable. The sheet's header is only the title, so the sheet at rest leaves most of the map.
    func testTheMapStaysUsableAtTheLargestTextSizes() async throws {
        for type in [DynamicTypeSize.accessibility3, .accessibility5] {
            let header = PlanSheetCompactHeader(title: "Current vehicle position → Las Vegas, Nevada").environment(\.dynamicTypeSize, type).padding(.horizontal, 16)
            let need = try await size(of: header, width: 393).height + 28
            for container in [CGFloat(700), 520] {   // a tall phone, and an SE with the bars taken off
                let sheet = SheetLayout.height(.peek, in: container, peekNeed: need)
                XCTAssertGreaterThanOrEqual(container - sheet, container * 0.45, "\(type), \(container) pt: at least 45 % of the map shows over the resting sheet")
            }
        }
    }

    func testTheDayChipsWrapAndEachIsAFullTapTargetAtTheLargestTextSize() async throws {
        for type in [DynamicTypeSize.large, .accessibility3, .accessibility5] {
            let chips = PlanDayChips(days: [1, 2], day: .constant(nil)).environment(\.dynamicTypeSize, type)
            let s = try await size(of: chips, width: 393)
            XCTAssertLessThanOrEqual(s.width, 393, "\(type): no chip runs off the edge (Day 2 was clipped to D...)")
            XCTAssertGreaterThanOrEqual(s.height, 44, "\(type): a full-size tap target")
        }
        let big = try await size(of: PlanDayChips(days: [1, 2], day: .constant(nil)).environment(\.dynamicTypeSize, .accessibility5), width: 393)
        XCTAssertGreaterThan(big.height, 44 * 1.5, "at AX5 they wrap onto rows")
    }

    func testTheSheetNeverRestsShorterThanItsHeader() {
        let h: CGFloat = 700
        XCTAssertEqual(SheetLayout.height(.peek, in: h), 150, "not measured yet: the old peek")
        XCTAssertEqual(SheetLayout.height(.half, in: h), 322)
        XCTAssertGreaterThanOrEqual(SheetLayout.height(.peek, in: h, peekNeed: 200), 200, "a large-text title fits the peek")
        XCTAssertGreaterThanOrEqual(SheetLayout.height(.half, in: h, headerNeed: 400), 400, "a large-text header (title, summary, Edit, day chips) fits at half")
        XCTAssertEqual(SheetLayout.height(.peek, in: h, peekNeed: 900), 245, accuracy: 0.001, "capped: the peek is not allowed to take the map")
        XCTAssertEqual(SheetLayout.height(.half, in: h, headerNeed: 900), 455, "capped at 65 %")
        XCTAssertEqual(SheetLayout.height(.full, in: h, headerNeed: 400), 692)
    }
}
