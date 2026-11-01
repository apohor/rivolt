import SwiftUI
import XCTest
@testable import Rivolt

/// The gradient legend card on a 343 pt map card (the 375 pt SE), light and dark, miles and kilometres, at the default and the largest text sizes.
@MainActor
final class GradientLegendSnapshotTests: XCTestCase {
    private func card(_ lo: Double, _ hi: Double) -> some View {
        ZStack(alignment: .bottomLeading) {
            RoundedRectangle(cornerRadius: Theme.radiusCard).fill(Theme.card)
            GradientLegend(lo: lo, hi: hi).padding(4)
        }
        .frame(width: 343, height: 90)
    }

    func testRender() throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            for (label, type) in [("default", DynamicTypeSize.large), ("ax3", .accessibility3)] {
                let page = VStack(spacing: 12) {
                    card(32, 55)
                    card(0, 78)
                    card(104.6, 187.3)
                }
                .padding(12).frame(width: 367).background(ThemePageBackground())
                let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, type))
                renderer.scale = 2
                let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "legend did not render").pngData())
                let file = "gradient-legend-\(scheme == .dark ? "dark" : "light")-\(label).png"
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
