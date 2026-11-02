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
            PinPillsOverlay(size: size, start: start, end: end, startTime: startTime, endTime: endTime, legend: true, startLine: [start, end], endLine: [end, start])
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

    /// A pin right beside the legend card (the real one, with its mode switch, sized by the system at the text size) and beside the credit: the
    /// pills keep clear of both, at the default and a large text size.
    func testRenderNextToTheLegendAndTheCredit() throws {
        Theme.registerFonts()
        let size = CGSize(width: 343, height: 220)
        for scheme in [ColorScheme.light, .dark] {
            for (label, type) in [("default", DynamicTypeSize.large), ("xxxl", .xxxLarge)] {
                // The legend and the credit as drawn, measured: what the map measures in its own space.
                func sized<V: View>(_ v: V) -> CGSize {
                    UIHostingController(rootView: v.environment(\.dynamicTypeSize, type).environment(\.colorScheme, scheme)).sizeThatFits(in: CGSize(width: size.width, height: 400))
                }
                let legend = GradientLegend(lo: 11, hi: 58, metric: .speed, metrics: [.speed, .elevation], selection: .constant(.speed))
                let ls = sized(legend.padding(4)), cs = sized(MapAttribution())
                let legendFrame = CGRect(x: 0, y: size.height - ls.height, width: ls.width, height: ls.height).insetBy(dx: -4, dy: -4)
                let creditFrame = CGRect(x: size.width - cs.width, y: size.height - cs.height, width: cs.width, height: cs.height).insetBy(dx: -4, dy: -4)
                let start = CGPoint(x: legendFrame.maxX - 8, y: size.height - 24)
                let end = CGPoint(x: size.width - 70, y: size.height - 50)
                let card = ZStack(alignment: .topLeading) {
                    RoundedRectangle(cornerRadius: Theme.radiusCard).fill(Theme.card)
                    Path { p in p.move(to: start); p.addLine(to: CGPoint(x: 150, y: 70)); p.addLine(to: end) }.stroke(RouteMapView.color(.to50), lineWidth: 3)
                    Circle().fill(Theme.routeStart).frame(width: 12, height: 12).position(start)
                    Circle().fill(Theme.routeEnd).frame(width: 12, height: 12).position(end)
                    PinPillsOverlay(size: size, start: start, end: end, startTime: "5:54 PM", endTime: "6:13 PM", legend: true, obstacles: [legendFrame, creditFrame],
                                    startLine: [start, CGPoint(x: 150, y: 70)], endLine: [end, CGPoint(x: 150, y: 70)])
                    legend.padding(4).frame(width: size.width, height: size.height, alignment: .bottomLeading)
                    MapAttribution().frame(width: size.width, height: size.height, alignment: .bottomTrailing)
                }
                .frame(width: size.width, height: size.height)
                let page = card.padding(12).frame(width: 367).background(ThemePageBackground())
                let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, type))
                renderer.scale = 2
                let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "pills did not render").pngData())
                let file = "pin-pills-legend-\(scheme == .dark ? "dark" : "light")-\(label).png"
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
