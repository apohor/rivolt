import SwiftUI
import XCTest
@testable import Rivolt

/// The drive map's layout: a clean map (time-only pills, the credit, the expand mark) and the legend card directly under it with the mode
/// switch, the range, the full-width bar and both times. A three-mode drive whose end sits low on the map, light and dark, default and large text.
@MainActor
final class MapLayoutSnapshotTests: XCTestCase {
    private func drawing() -> RouteSnap.Drawing {
        let coords = (0..<8).map { RouteSnap.Coord(lat: 40 + Double($0) * 0.001, lon: -100 + Double($0) * 0.0004) }
        let run = RouteSnap.Run(coords: coords, speeds: [12, 30, 45, 64, 58, 40, 30, 20],
                                extras: [.efficiency: [2.0, 2.4, 3.0, 3.6, 3.4, 3.0, 2.6, 2.2], .elevation: [100, 120, 160, 220, 300, 380, 420, 450]])
        return RouteSnap.Drawing(pieces: [], routedGaps: [], gaps: [], start: nil, end: nil, hasSpeed: true, runs: [run])
    }

    func testRender() throws {
        Theme.registerFonts()
        let d = drawing()
        XCTAssertEqual(d.availableMetrics, RouteGradient.Metric.allCases, "the three modes")
        let mapSize = CGSize(width: 343, height: 250)
        let start = CGPoint(x: 150, y: 40), end = CGPoint(x: 215, y: 205)
        let route = [start, CGPoint(x: 170, y: 120), end]
        let credit = CGRect(x: mapSize.width - 120, y: mapSize.height - 26, width: 120, height: 26)
        let expand = CGRect(x: mapSize.width - 40, y: 0, width: 40, height: 40)
        for scheme in [ColorScheme.light, .dark] {
            for (label, type) in [("default", DynamicTypeSize.large), ("ax3", .accessibility3)] {
                let map = ZStack(alignment: .topLeading) {
                    RoundedRectangle(cornerRadius: Theme.radiusCard).fill(Theme.card)
                    Path { p in p.move(to: route[0]); p.addLine(to: route[1]); p.addLine(to: route[2]) }.stroke(RouteMapView.color(.to50), lineWidth: 3.5)
                    Circle().fill(Theme.routeStart).frame(width: 12, height: 12).position(start)
                    Circle().fill(Theme.routeEnd).frame(width: 12, height: 12).position(end)
                    PinPillsOverlay(size: mapSize, start: start, end: end, startTime: "12:46 AM", endTime: "1:06 AM", legend: false,
                                    obstacles: [credit, expand], startLine: [start, route[1]], endLine: [end, route[1]])
                    ExpandMark().frame(width: mapSize.width, height: mapSize.height, alignment: .topTrailing)
                    MapAttribution().frame(width: mapSize.width, height: mapSize.height, alignment: .bottomTrailing)
                }
                .frame(width: mapSize.width, height: mapSize.height)
                .clipShape(RoundedRectangle(cornerRadius: Theme.radiusCard))
                let page = VStack(spacing: 8) {
                    map
                    RouteLegendCard(drawing: d, ready: true, metric: .speed, metrics: RouteGradient.Metric.allCases, selection: .constant(.speed),
                                    times: ("12:46 AM", "1:06 AM"))
                }
                .padding(12).frame(width: 367).background(ThemePageBackground())
                let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, type))
                renderer.scale = 2
                let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "layout did not render").pngData())
                let file = "map-layout-\(scheme == .dark ? "dark" : "light")-\(label).png"
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

    func testTheTimesLine() {
        XCTAssertEqual(RouteLegendCard.timesLine(("12:46 AM", "1:06 AM")), "12:46 AM → 1:06 AM")
        XCTAssertNil(RouteLegendCard.timesLine(("12:46 AM", nil)))
        XCTAssertNil(RouteLegendCard.timesLine(nil))
    }
}
