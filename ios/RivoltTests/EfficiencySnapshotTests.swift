import SwiftUI
import XCTest
@testable import Rivolt

/// Activity → Efficiency as it draws, from the synthetic fixture: light, dark, an accessibility text size (rows), and
/// the empty state. Renders land in SNAPSHOT_DIR (the ios-snapshots artifact) for review next to the web.
@MainActor
final class EfficiencySnapshotTests: XCTestCase {
    private func page(_ shown: EfficiencyModel.Shown) -> some View {
        VStack(alignment: .leading, spacing: 16) {
            ThemeSegmented(options: ActivityMode.allCases.map { ThemeSegmented<ActivityMode>.Option(id: $0, label: $0.label) }, selection: .constant(.efficiency),
                           accessibilityName: "View", fillsWidth: true)
            EfficiencyView(shown: shown, monthName: "October 2026")
        }
        .padding(16)
        .frame(width: 390, alignment: .leading)
        .themePage()
    }

    private func notEnough() throws -> EfficiencyInsights {
        var root = try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data("efficiency")) as? [String: Any])
        var total = try XCTUnwrap(root["total"] as? [String: Any])
        total["not_enough_data"] = true
        root["total"] = total
        return try JSON.decoder().decode(EfficiencyInsights.self, from: JSONSerialization.data(withJSONObject: root))
    }

    func testRenderEfficiency() throws {
        Theme.registerFonts()
        let e = try Fixture.decode(EfficiencyInsights.self, "efficiency")
        let renders: [(String, AnyView)] = [
            ("efficiency-light", AnyView(page(.insights(e)).environment(\.colorScheme, .light))),
            ("efficiency-dark", AnyView(page(.insights(e)).environment(\.colorScheme, .dark))),
            ("efficiency-accessibility-light", AnyView(page(.insights(e)).environment(\.colorScheme, .light).environment(\.dynamicTypeSize, .accessibility3))),
            ("efficiency-empty-light", AnyView(page(.insights(try notEnough())).environment(\.colorScheme, .light))),
            ("efficiency-unavailable-dark", AnyView(page(.unavailable).environment(\.colorScheme, .dark))),
        ]
        for (name, content) in renders {
            let renderer = ImageRenderer(content: content.fixedSize(horizontal: false, vertical: true))
            // A page at accessibility text sizes is several screens tall: at 3x its bitmap is past what PNG encoding takes.
            renderer.scale = name.contains("accessibility") ? 1.5 : 3
            let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
            XCTAssertGreaterThan(image.size.height, 100, name)
            let png = try XCTUnwrap(image.pngData())
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = "\(name).png"
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
            }
        }
    }
}
