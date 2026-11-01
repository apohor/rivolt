import SwiftUI
import XCTest
@testable import Rivolt

/// The full-screen map's chrome (Done, title, recenter) over a stand-in for the map, on an iPhone-sized screen, light and dark, at the default and
/// the largest text sizes.
@MainActor
final class FullScreenMapSnapshotTests: XCTestCase {
    func testRender() throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            for (label, type) in [("default", DynamicTypeSize.large), ("ax3", .accessibility3)] {
                let screen = FullScreenMapScreen(title: "Drive route", recenterLabel: "Recenter on the route") { _ in
                    ZStack(alignment: .bottomLeading) {
                        Theme.card
                        Path { p in p.move(to: CGPoint(x: 60, y: 420)); p.addLine(to: CGPoint(x: 300, y: 120)) }.stroke(RouteMapView.color(.to50), lineWidth: 3)
                        GradientLegend(lo: 12, hi: 64).padding(4)
                    }
                }
                .frame(width: 390, height: 640)
                .environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, type)
                let renderer = ImageRenderer(content: screen)
                renderer.scale = 2
                let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "full-screen map did not render").pngData())
                let file = "full-screen-map-\(scheme == .dark ? "dark" : "light")-\(label).png"
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
