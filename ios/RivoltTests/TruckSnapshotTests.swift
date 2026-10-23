import SwiftUI
import XCTest
@testable import Rivolt

/// The Truck tab drawn from the shared fixtures, next to qa's web references (Truck at 390 px, light and dark): parked
/// drain (7 and 30 days), the lease, pack capacity, trends, range, sleep and idle-awake, and the vehicle card.
@MainActor
final class TruckSnapshotTests: XCTestCase {
    private var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "America/Chicago")!
        return c
    }

    private let now = JSON.parseDate("2026-10-22T17:00:00Z")!

    private func lowTires() throws -> VehicleState {
        var o = try XCTUnwrap(JSONSerialization.jsonObject(with: Fixture.data("state")) as? [String: Any])
        o["tire_pressure_fl_bar"] = 2.1
        o["tire_pressure_rr_bar"] = 2.7
        o["tire_pressure_status_rr"] = "Low"
        o["ota_status"] = "Ready_To_Install"
        o["ota_available_version"] = "2026.38.0"
        return try JSON.decoder().decode(VehicleState.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private func model(state: VehicleState? = nil) throws -> TruckModel {
        TruckModel(state: try state ?? Fixture.decode(VehicleState.self, "state"),
                   drain: .value(try Fixture.decode(DrainSnapshot.self, "drain")),
                   pack: .value(try Fixture.decode(PackHealth.self, "pack_health")),
                   trends: .value(try Fixture.decode(TruckTrends.self, "trends")),
                   cold: .value(try Fixture.decode(ColdRange.self, "cold_range")),
                   lease: .value(try Fixture.decode(LeaseStatus.self, "lease_status")),
                   leaseTerms: try Fixture.decode(LeaseTerms.self, "lease_terms"),
                   sleep: .value(try Fixture.decode([DayActivity].self, "sleep_activity")))
    }

    private func page(_ key: TruckLogic.DrainKey, state: VehicleState? = nil) throws -> some View {
        let m = try model(state: state)
        let v = try Fixture.decode([Vehicle].self, "vehicles")[0]
        return VStack(spacing: 0) {
            ThemeTopBar { EmptyView() }
            HStack {
                Text("Truck").themeText(.hero)
                Spacer()
                ThemeStepper(title: "Oct 2026", canGoForward: false, previous: {}, next: {})
            }
            .padding(.horizontal, 16).padding(.vertical, 8)
            TruckContent(model: m, vehicle: v, drainKey: .constant(key), temp: 15.6, pickTemp: { _ in }, r2Dismissed: .constant(false),
                         month: PeriodMonth(containing: now, calendar: cal), now: now, calendar: cal, metric: false)
                .padding(16)
        }
        .frame(width: 390)
        .background(ThemePageBackground())
    }

    private func render(_ name: String, _ content: some View) throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: content.environment(\.colorScheme, scheme).fixedSize(horizontal: false, vertical: true))
            renderer.scale = 3
            let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
            XCTAssertGreaterThan(image.size.height, 1500)
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

    func testRenderTheTruckTab() throws {
        try render("truck-7d", try page(.week))
        try render("truck-30d", try page(.month))
        try render("truck-tires-low", try page(.week, state: try lowTires()))
    }
}
