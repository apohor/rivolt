import SwiftUI
import XCTest
@testable import Rivolt

@MainActor
final class MapObstaclesTests: XCTestCase {
    func testTheCameraKeepsAboveTheTallestThingAtTheBottom() {
        let legend = CGRect(x: 0, y: 130, width: 215, height: 90)   // the card with its three-mode switch, 4 pt of air included
        let credit = CGRect(x: 230, y: 190, width: 113, height: 30)
        let expand = CGRect(x: 300, y: 0, width: 40, height: 40)    // top: not a bottom obstacle
        XCTAssertEqual(RouteMapView.bottomInset(obstacles: [legend, credit, expand], mapHeight: 220), 90 + 8)
        XCTAssertEqual(RouteMapView.bottomInset(obstacles: [credit], mapHeight: 220), 30 + 8)
        XCTAssertEqual(RouteMapView.bottomInset(obstacles: nil, mapHeight: 220), 0, "not measured yet")
        XCTAssertEqual(RouteMapView.bottomInset(obstacles: [legend], mapHeight: 0), 0)
    }

    /// The frames are measured in the map's own space, which has to contain the overlays that report them (the first version declared it on
    /// the map under them and every frame came back wrong, so the pills landed on the legend).
    func testAnOverlayIsMeasuredWhereItIsDrawn() throws {
        var got: [CGRect] = []
        let view = Color.clear.frame(width: 300, height: 200)
            .overlay(alignment: .bottomLeading) { Color.red.frame(width: 100, height: 40).measured() }
            .coordinateSpace(name: MapObstacles.space)
            .onPreferenceChange(MapObstacles.Key.self) { got = $0 }
        let host = UIHostingController(rootView: view)
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 300, height: 200))
        window.rootViewController = host
        window.makeKeyAndVisible()
        host.view.layoutIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.3))
        let frame = try XCTUnwrap(got.first)
        XCTAssertEqual(frame.minX, 0, accuracy: 1)
        XCTAssertEqual(frame.minY, 160, accuracy: 1)
        XCTAssertEqual(frame.width, 100, accuracy: 1)
        XCTAssertEqual(frame.height, 40, accuracy: 1)
    }
}
