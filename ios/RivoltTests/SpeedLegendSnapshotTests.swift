import SwiftUI
import XCTest
@testable import Rivolt

/// The drive map's speed legend, before and after the owner's "fix map labels": on the smallest supported width (375 pt, so a 343 pt map
/// card), in light and dark, at the default and the largest text sizes. The "before" is the old layout kept here only to draw it.
@MainActor
final class SpeedLegendSnapshotTests: XCTestCase {
    private let all = RouteSnap.Speed.allCases

    /// The old layout: 22 pt cells with the labels centred in them, and "mph" on its own beside the bar.
    private struct OldLegend: View {
        let bands: [RouteSnap.Speed]
        private let cell: CGFloat = 22
        var body: some View {
            HStack(alignment: .bottom, spacing: 4) {
                Text("mph").foregroundStyle(Theme.textTertiary)
                VStack(alignment: .leading, spacing: 1) {
                    HStack(spacing: 1) {
                        ForEach(bands, id: \.rawValue) { b in RoundedRectangle(cornerRadius: 1.5).fill(RouteMapView.color(b)).frame(width: cell, height: 3) }
                    }
                    HStack(spacing: 1) { ForEach(bands, id: \.rawValue) { b in Text(b.shortLabel).frame(width: cell) } }
                }
            }
            .mapCaption()
            .foregroundStyle(Theme.textBody)
            .padding(.horizontal, 4).padding(.vertical, 2)
            .background(Theme.background.opacity(0.78), in: RoundedRectangle(cornerRadius: 5))
            .overlay(RoundedRectangle(cornerRadius: 5).strokeBorder(Theme.border, lineWidth: 1))
        }
    }

    private func card<L: View>(_ legend: L) -> some View {
        ZStack(alignment: .bottomLeading) {
            RoundedRectangle(cornerRadius: Theme.radiusCard).fill(Theme.card)
            legend.padding(4)
        }
        .frame(width: 343, height: 120)
    }

    func testRenderBeforeAndAfter() throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            for (label, size) in [("default", DynamicTypeSize.large), ("xxxl", .xxxLarge), ("ax3", .accessibility3)] {
                let page = VStack(alignment: .leading, spacing: 12) {
                    Text("Before").font(.caption)
                    card(OldLegend(bands: all))
                    Text("After").font(.caption)
                    card(SpeedLegend(bands: all))
                    card(SpeedLegend(bands: [.to25, .to50, .over65]))
                }
                .padding(12).frame(width: 367).background(ThemePageBackground())
                let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, size))
                renderer.scale = 2
                let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "legend did not render").pngData())
                let file = "speed-legend-\(scheme == .dark ? "dark" : "light")-\(label).png"
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

    func testTheUnitIsInTheTitleNotBesideTheBar() {
        XCTAssertEqual(SpeedLegend.title, "Speed, mph")
    }
}
