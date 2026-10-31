import SwiftUI
import XCTest
@testable import Rivolt

/// The free account's Pro sheet: no trial anywhere, the renewal terms, and the plain demo links.
@MainActor
final class ProPaywallSnapshotTests: XCTestCase {
    func testRenderTheFreeAccountsSheet() throws {
        Theme.registerFonts()
        let page = ProPaywallView(plan: .free)
            .environment(AppState())
            .frame(width: 390, height: 1100)
            .background(ThemePageBackground())
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme))
            renderer.scale = 2
            let image = try XCTUnwrap(renderer.uiImage, "the sheet did not render")
            let png = try XCTUnwrap(image.pngData())
            let file = "pro-sheet-free-\(scheme == .dark ? "dark" : "light").png"
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
