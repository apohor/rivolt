import SwiftUI
import XCTest
@testable import Rivolt

/// The detail cards at the largest accessibility text size, rendered from the shared fixtures. CI uploads the PNGs next
/// to the other snapshots; the check is looking at them (no clipping, one column, stacked rows).
@MainActor
final class NowDetailAccessibilityTests: XCTestCase {
    func testRenderAtAnAccessibilitySize() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let clock = try XCTUnwrap(now.at).addingTimeInterval(120)
        let summary = NowSummary(state: st, now: now, clock: clock)
        let charge = try JSON.decoder().decode([ChargeBrief].self, from: Data(#"[{"ID":"h","EnergyAddedKWh":1,"Cost":0,"kind":"ac","place":"home"}]"#.utf8)).first
        let session = try Fixture.decode(LiveSession.self, "live_session_home")
        let charging = try XCTUnwrap(ChargingDetail.make(summary: summary, session: session, charge: charge))
        try render("detail-charging-ax", ChargingDetailCard(detail: charging))
        let parked = NowSummary(input: .init(socPct: 70, rangeKm: 250, chargeTargetPct: nil, gear: "P", powerState: "ready", chargerState: nil, chargerPowerKw: nil, at: clock), now: nil, clock: clock)
        let last = try Fixture.decode([DriveBrief].self, "drives_last").first
        let drive = try XCTUnwrap(DriveDetail.make(summary: parked, live: nil, last: last))
        try render("detail-last-drive-ax", DriveDetailCard(detail: drive, clock: try XCTUnwrap(last?.endedAt).addingTimeInterval(54 * 60)))
    }

    private func render(_ name: String, _ card: some View) throws {
        for scheme in [ColorScheme.light, .dark] {
            let view = card
                .padding()
                .frame(width: 393)
                .background(Color(.systemGroupedBackground))
                .environment(\.colorScheme, scheme)
                .environment(\.dynamicTypeSize, .accessibility3)
            let renderer = ImageRenderer(content: view)
            renderer.scale = 2
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
}
