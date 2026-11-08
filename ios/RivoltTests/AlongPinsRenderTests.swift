import MapLibre
import SwiftUI
import XCTest
@testable import Rivolt

/// The chargers, hotels and campgrounds along a planned route are drawn by a real MapLibre view in a window: a feature of each kind has to
/// come out of the "along" circle layer, not only be handed to it (the planner's layer chips).
@MainActor
final class AlongPinsRenderTests: XCTestCase {
    private let style = ##"{"version":8,"sources":{},"layers":[{"id":"bg","type":"background","paint":{"background-color":"#101010"}}]}"##

    private func drawing(kinds: [Int], emphasis: Bool = false) -> RouteSnap.Drawing {
        let line = (0..<20).map { RouteSnap.Coord(lat: 40 + Double($0) * 0.01, lon: -100 + Double($0) * 0.01) }
        var d = RouteSnap.Drawing(pieces: [RouteSnap.Piece(coords: line, speed: nil)], routedGaps: [], gaps: [], start: line.first, end: line.last, hasSpeed: false)
        d.along = kinds.enumerated().map { i, k in
            RouteSnap.Drawing.Stop(coord: line[3 + i * 3], label: "", emphasis: emphasis, kind: k)
        }
        return d
    }

    /// How many "along" pins a map in a window draws for `d`, optionally after the drawing changes to `later`.
    private func drawn(_ d: RouteSnap.Drawing, then later: RouteSnap.Drawing? = nil) async throws -> Int {
        let coord = RouteMapView.Coordinator(drawing: d, onHealth: { _ in })
        let map = MLNMapView.displayOnly(styleJSON: style, watch: coord, interactive: false)
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first, "a window scene to draw in")
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 600)
        map.frame = window.bounds
        window.addSubview(map)
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        for _ in 0..<100 where map.style?.layer(withIdentifier: "rivolt-route-along") == nil { try await Task.sleep(for: .milliseconds(100)) }
        XCTAssertNotNil(map.style?.layer(withIdentifier: "rivolt-route-along"), "the along layer is added")
        map.layoutIfNeeded()
        try await Task.sleep(for: .seconds(3))
        if let later { coord.update(later, on: map) }
        var n = 0
        for _ in 0..<60 {
            try await Task.sleep(for: .milliseconds(100))
            n = map.visibleFeatures(in: map.bounds, styleLayerIdentifiers: ["rivolt-route-along"]).count
            if n > 0 { break }
        }
        return n
    }

    func testEachKindOfPinIsDrawn() async throws {
        for kind in 0...3 {
            let n = try await drawn(drawing(kinds: [kind, kind, kind]))
            XCTAssertEqual(n, 3, "kind \(kind) (0 fast, 1 hotel, 2 camping, 3 medium): its pins are drawn")
        }
    }

    func testAPreferredPinIsDrawnToo() async throws {
        let n = try await drawn(drawing(kinds: [0, 0], emphasis: true))
        XCTAssertEqual(n, 2)
    }

    func testPinsThatArriveAfterTheRouteAreDrawn() async throws {
        let n = try await drawn(drawing(kinds: []), then: drawing(kinds: [0, 0, 1, 2, 3]))
        XCTAssertEqual(n, 5, "the layer's answer comes after the route is already on the map")
    }
}
