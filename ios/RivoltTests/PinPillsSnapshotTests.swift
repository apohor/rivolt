import SwiftUI
import XCTest
@testable import Rivolt

/// The Start and End pills on a 343 pt map card (the 375 pt SE), over a stand-in for the map: a plain card with the route drawn as a line
/// between the two pins and the speed legend in its corner. Light and dark, at the default and the largest text sizes.
@MainActor
final class PinPillsSnapshotTests: XCTestCase {
    private func card(start: CGPoint, end: CGPoint, startTime: String, endTime: String) -> some View {
        let size = CGSize(width: 343, height: 220)
        return ZStack(alignment: .topLeading) {
            RoundedRectangle(cornerRadius: Theme.radiusCard).fill(Theme.card)
            Path { p in p.move(to: start); p.addLine(to: end) }.stroke(RouteMapView.color(.to50), lineWidth: 3)
            Circle().fill(Theme.routeStart).frame(width: 12, height: 12).position(start)
            Circle().fill(Theme.routeEnd).frame(width: 12, height: 12).position(end)
            PinPillsOverlay(size: size, start: start, end: end, startTime: startTime, endTime: endTime, legend: true)
            SpeedLegend(bands: RouteSnap.Speed.allCases).padding(4).frame(width: size.width, height: size.height, alignment: .bottomLeading)
        }
        .frame(width: size.width, height: size.height)
    }

    func testRender() throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            for (label, type) in [("default", DynamicTypeSize.large), ("xxxl", .xxxLarge)] {
                let page = VStack(spacing: 12) {
                    card(start: CGPoint(x: 60, y: 170), end: CGPoint(x: 290, y: 50), startTime: "9:15 PM", endTime: "11:28 PM")
                    card(start: CGPoint(x: 290, y: 60), end: CGPoint(x: 70, y: 40), startTime: "9:15 PM", endTime: "11:28 PM")
                    card(start: CGPoint(x: 170, y: 100), end: CGPoint(x: 176, y: 104), startTime: "9:15 PM", endTime: "11:28 PM")
                }
                .padding(12).frame(width: 367).background(ThemePageBackground())
                let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, type))
                renderer.scale = 2
                let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "pills did not render").pngData())
                let file = "pin-pills-\(scheme == .dark ? "dark" : "light")-\(label).png"
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
