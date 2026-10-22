import SwiftUI
import XCTest
@testable import Rivolt

/// The rates card in each state it can be in.
@MainActor
final class ChargingRatesSnapshotTests: XCTestCase {
    private let url = URL(string: "https://rivolt.dev")!
    private let saved = ChargingRates(homePricePerKwh: 0.15, homeCurrency: "USD")

    private func card(_ load: ChargingRatesModel.LoadState, _ save: ChargingRatesModel.SaveState = .idle) -> some View {
        ChargingRatesContent(model: ChargingRatesModel(load: load, save: save), serverURL: url, onSave: { _, _ in }, onReload: {})
    }

    func testRenderTheStates() throws {
        Theme.registerFonts()
        let page = VStack(alignment: .leading, spacing: 16) {
            card(.loaded(saved))
            card(.loaded(.init(homePricePerKwh: 0.2, homeCurrency: "USD")), .saved(.init(homePricePerKwh: 0.2, homeCurrency: "USD")))
            card(.loaded(saved), .failed(.notAllowed))
            card(.loaded(saved), .failed(.tooMany))
            card(.loaded(saved), .failed(.unknown))
            card(.failed)
        }
        .padding(16).frame(width: 390).background(ThemePageBackground())
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).fixedSize(horizontal: false, vertical: true))
            renderer.scale = 2
            let image = try XCTUnwrap(renderer.uiImage, "rates did not render")
            let png = try XCTUnwrap(image.pngData())
            let file = "charging-rates-\(scheme == .dark ? "dark" : "light").png"
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
