import SwiftUI
import XCTest
@testable import Rivolt

/// The theme's building blocks rendered together, light and dark, as PNGs CI uploads: the page, labels, the type
/// scale, a card, the hero at rest / charging / driving, the status pills and the solid button. This is the sheet the
/// restyled screens are compared against (qa's web screenshots at 390 px).
@MainActor
final class ThemeSnapshotTests: XCTestCase {
    private var sheet: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Theme").themeText(.label)
            VStack(alignment: .leading, spacing: 6) {
                Text("Hero 36").themeText(.hero)
                Text("Title 24").themeText(.title)
                Text("Value 14, body text").themeText(.value)
                Text("Sub-line 12 and secondary").themeText(.subline)
                Text("Caption 11").themeText(.caption)
            }
            .themeCard()
            HStack(spacing: 8) {
                ThemePill(text: "Live", tone: .live)
                ThemePill(text: "Last seen 3 h ago", tone: .stale)
                ThemePill(text: "Asleep", tone: .neutral)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("R1S · Adventure").themeText(.label)
                Text("78%").themeText(.hero)
                Text("Time left 42 min").font(Theme.mono(ThemeTokens.typeValue)).foregroundStyle(Theme.textBody)
            }
            .themeHero()
            VStack(alignment: .leading, spacing: 4) {
                Text("Charging").themeText(.label)
                Text("148.6 kW").font(Theme.mono(ThemeTokens.typeHero, .semibold)).foregroundStyle(Theme.textPrimary)
            }
            .themeHero(live: .charging)
            VStack(alignment: .leading, spacing: 4) {
                Text("Driving").themeText(.label)
                Text("46 mph").themeText(.hero)
            }
            .themeHero(live: .driving)
            VStack(alignment: .leading, spacing: 4) {
                Text("Battery low").themeText(.label)
                Text("9%").themeText(.hero, color: Theme.critical)
            }
            .themeHero(critical: true)
            Button("Turn on") {}.buttonStyle(ThemeButtonStyle())
        }
        .padding(16)
        .frame(width: 390, alignment: .leading)
        .fixedSize(horizontal: false, vertical: true)
        .themePage()
    }

    func testRenderTheSheet() throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: sheet.environment(\.colorScheme, scheme))
            renderer.scale = 2
            let image = try XCTUnwrap(renderer.uiImage, "theme sheet did not render")
            XCTAssertGreaterThan(image.size.height, 400)
            let png = try XCTUnwrap(image.pngData())
            let file = "theme-\(scheme == .dark ? "dark" : "light").png"
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
