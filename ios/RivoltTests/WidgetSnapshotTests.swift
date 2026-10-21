import SwiftUI
import WidgetKit
import XCTest
@testable import Rivolt

/// Renders the widgets from the shared fixtures to PNGs (every family, fresh and
/// stale, light and dark) so the look can be reviewed from CI without a device.
/// Like the Now card renders: it asserts only that each view renders.
@MainActor
final class WidgetSnapshotTests: XCTestCase {
    private let families: [(name: String, family: WidgetFamily, size: CGSize)] = [
        ("small", .systemSmall, CGSize(width: 158, height: 158)),
        ("medium", .systemMedium, CGSize(width: 338, height: 158)),
        ("lock-circular", .accessoryCircular, CGSize(width: 76, height: 76)),
        ("lock-rectangular", .accessoryRectangular, CGSize(width: 172, height: 76)),
        ("lock-inline", .accessoryInline, CGSize(width: 257, height: 22)),
    ]

    private func snapshot() throws -> (WidgetSnapshot, Date) {
        let input = NowSummary.Input(try Fixture.decode(VehicleState.self, "state"))
        let now = try Fixture.decode(NowSnapshot.self, "now_v1")
        let at = try XCTUnwrap(now.at)
        return (try XCTUnwrap(WidgetSnapshot.make(input: input, now: now, clock: at)), at)
    }

    func testRenderEveryFamily() throws {
        let (snap, at) = try snapshot()
        let cases: [(String, WidgetContent, Date)] = [
            ("fresh", .data(snap), at.addingTimeInterval(120)),
            ("stale", .data(snap), at.addingTimeInterval(3 * 3600)),
            ("signedout", .signedOut, at),
        ]
        for (label, content, date) in cases {
            for f in families {
                try render("widget-\(f.name)-\(label)", NowWidgetView(entry: NowEntry(date: date, content: content), family: f.family), size: f.size, lock: f.name.hasPrefix("lock"))
            }
        }
    }

    private func render(_ name: String, _ widget: NowWidgetView, size: CGSize, lock: Bool) throws {
        for scheme in [ColorScheme.light, .dark] {
            let view = widget
                .padding(lock ? 0 : 16)
                .frame(width: size.width, height: size.height)
                .background(lock ? Color.black.opacity(0.6) : Color(.secondarySystemBackground))
                .environment(\.colorScheme, lock ? .dark : scheme)
            let renderer = ImageRenderer(content: view)
            renderer.scale = 3
            let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
            XCTAssertGreaterThan(image.size.width, 10)
            let png = try XCTUnwrap(image.pngData())
            let file = "\(name)-\(scheme == .dark ? "dark" : "light").png"
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = file
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
            }
            if lock { break }
        }
    }
}
