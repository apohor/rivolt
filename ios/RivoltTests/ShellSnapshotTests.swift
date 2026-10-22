import SwiftUI
import XCTest
@testable import Rivolt

/// The shell and the Now card, rendered next to qa's web references (Now at 390 px, light and dark): the top bar, the
/// hero in each state the web was captured in, and the tab bar with all five of the web's tabs.
@MainActor
final class ShellSnapshotTests: XCTestCase {
    private let tabs = [
        ThemeTabItem(id: "now", label: "Now", icon: "now"), ThemeTabItem(id: "activity", label: "Activity", icon: "activity"),
        ThemeTabItem(id: "money", label: "Money", icon: "money"), ThemeTabItem(id: "plan", label: "Plan", icon: "plan"),
        ThemeTabItem(id: "truck", label: "Truck", icon: "truck"),
    ]

    private func page(_ content: some View) -> some View {
        VStack(spacing: 0) {
            ThemeTopBar { Image(systemName: "person.crop.circle").font(.title3).foregroundStyle(Theme.textSecondary) }
            VStack(spacing: 16) {
                HStack(alignment: .firstTextBaseline) { Text("Now").themeText(.hero); Spacer() }
                content
            }
            .padding(16)
            Spacer(minLength: 0)
            ThemeTabBar(items: tabs, selection: .constant("now"))
        }
        .frame(width: 390)
        .background(ThemePageBackground())
    }

    private func vehicle() throws -> Vehicle { try Fixture.decode([Vehicle].self, "vehicles")[0] }

    private func summary(_ kind: String, soc: Double, kph: Double? = nil, power: Double? = nil, target: Double? = 80, age: TimeInterval = 20) -> NowSummary {
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let input = NowSummary.Input(
            socPct: soc, rangeKm: 325, chargeTargetPct: target,
            gear: kind == "driving" ? "D" : "P", powerState: kind == "asleep" ? "sleep" : "ready",
            chargerState: kind == "charging" ? "charging_active" : nil, chargerPowerKw: power, at: at, packKwh: 131, speedKph: kph)
        return NowSummary(input: input, now: nil, clock: at.addingTimeInterval(age))
    }

    private func render(_ name: String, _ content: some View) throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: content.environment(\.colorScheme, scheme).fixedSize(horizontal: false, vertical: true))
            renderer.scale = 3
            let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
            XCTAssertGreaterThan(image.size.height, 200)
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
        }
    }

    func testRenderTheNowStates() throws {
        let v = try vehicle()
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        func card(_ s: NowSummary, age: TimeInterval = 20) -> NowCard { NowCard(vehicle: v, summary: s, clock: at.addingTimeInterval(age)) }
        try render("shell-now-charging", page(card(summary("charging", soc: 52, power: 7.2))))
        try render("shell-now-driving", page(card(summary("driving", soc: 58, kph: 88))))
        try render("shell-now-parked", page(card(summary("parked", soc: 67))))
        try render("shell-now-asleep", page(card(summary("asleep", soc: 67))))
        try render("shell-now-stale", page(card(summary("parked", soc: 41, age: 3 * 3600), age: 3 * 3600)))
        try render("shell-now-low", page(card(summary("parked", soc: 9))))
    }
}
