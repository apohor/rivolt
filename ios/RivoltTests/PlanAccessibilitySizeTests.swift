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

    func testTheLayerChipsTakeMoreRowsAsTheTextGrowsAndAlwaysFitTheWidth() async throws {
        let model = TripPlanModel()
        model.setPreferred(["ea"])
        await model.setLayer(.chargers, on: true, source: Fake())
        var heights: [CGFloat] = []
        for type in [DynamicTypeSize.large, .accessibility3, .accessibility5] {
            let s = try await size(of: PlanLayerChips(model: model).environment(AppState()).environment(\.dynamicTypeSize, type), width: 393)
            XCTAssertLessThanOrEqual(s.width, 393, "\(type): the chips fit the width")
            heights.append(s.height)
        }
        XCTAssertGreaterThan(heights[0], 36 * 1.5, "at the normal size they take two rows, not one clipped row")
        XCTAssertGreaterThan(heights[1], heights[0], "more rows at a larger size")
    }

    func testTheLayerNoteIsAtMostTwoLinesAtTheLargestTextSize() async throws {
        let long = "Showing 304 of 627 · listing from 2026-11-01 · not live · and then a good deal more words so it would run on and on"
        let one = try await size(of: LayerNotePill(text: "Ag").environment(\.dynamicTypeSize, .accessibility5), width: 393).height
        let many = try await size(of: LayerNotePill(text: long).environment(\.dynamicTypeSize, .accessibility5), width: 393).height
        XCTAssertLessThan(many, one * 2.3, "two lines at most, not a third of the map")
    }

    func testALargeTextHeaderWithDayChipsIsTallerThanARestingSheetAndTheFloorCoversIt() async throws {
        let header = PlanSheetHeader(title: "To Crested Butte", summary: "980 mi · 2 days · 2 stops", onEdit: {}) {
            HStack { Text("Whole trip"); Text("Day 1"); Text("Day 2") }
        }
        .environment(\.dynamicTypeSize, .accessibility3)
        .padding(.horizontal, 16)
        let need = try await size(of: header, width: 393).height + 28
        XCTAssertGreaterThan(need, DetentSheet<EmptyView, EmptyView>.height(.half, in: 700), "this is the case that put Day 2 below the fold")
        XCTAssertGreaterThanOrEqual(DetentSheet<EmptyView, EmptyView>.height(.half, in: 700, floor: need), need)
        XCTAssertGreaterThanOrEqual(DetentSheet<EmptyView, EmptyView>.height(.peek, in: 700, floor: need), need)
    }

    func testTheSheetNeverRestsShorterThanItsHeader() {
        let h: CGFloat = 700
        XCTAssertEqual(DetentSheet<EmptyView, EmptyView>.height(.peek, in: h), 150, "normal text: unchanged")
        XCTAssertEqual(DetentSheet<EmptyView, EmptyView>.height(.half, in: h), 322)
        for d in [SheetDetent.peek, .half] {
            XCTAssertGreaterThanOrEqual(DetentSheet<EmptyView, EmptyView>.height(d, in: h, floor: 400), 400, "a large-text header (title, summary, Edit, day chips) fits")
        }
        XCTAssertEqual(DetentSheet<EmptyView, EmptyView>.height(.peek, in: h, floor: 900), 455, "capped: the header is not allowed to take the whole map")
        XCTAssertEqual(DetentSheet<EmptyView, EmptyView>.height(.full, in: h, floor: 400), 692)
    }
}
