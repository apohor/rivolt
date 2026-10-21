import SwiftUI
import XCTest
@testable import Rivolt

/// Renders the Now card from the shared fixtures to PNGs. CI passes
/// SNAPSHOT_DIR (TEST_RUNNER_SNAPSHOT_DIR) and uploads the files, so the look
/// can be reviewed without anyone building the app. Not a pixel diff: it
/// asserts only that the card renders at a sane size.
@MainActor
final class NowCardSnapshotTests: XCTestCase {
    func testRenderCharging() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let clock = try XCTUnwrap(now.at).addingTimeInterval(120)
        try snapshot("now-charging", NowCard(vehicle: vehicle, summary: NowSummary(state: st, now: now, clock: clock), clock: clock))
    }

    func testRenderStale() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let clock = try XCTUnwrap(now.at).addingTimeInterval(3 * 3600)
        try snapshot("now-stale", NowCard(vehicle: vehicle, summary: NowSummary(state: st, now: now, clock: clock), clock: clock))
    }

    func testRenderChargingAndDriveDetail() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let clock = try XCTUnwrap(now.at).addingTimeInterval(120)
        let summary = NowSummary(state: st, now: now, clock: clock)
        let charge = try Fixture.decode([ChargeBrief].self, "charges_last").first
        for (name, file) in [("detail-charging-fast", "live_session"), ("detail-charging-home", "live_session_home")] {
            let session = try Fixture.decode(LiveSession.self, file)
            let detail = try XCTUnwrap(ChargingDetail.make(summary: summary, session: session, charge: charge))
            try snapshot(name, ChargingDetailCard(detail: detail))
        }
        let parked = NowSummary(input: .init(socPct: 70, rangeKm: 250, chargeTargetPct: nil, gear: "P", powerState: "ready", chargerState: nil, chargerPowerKw: nil, at: clock), now: nil, clock: clock)
        let last = try Fixture.decode([DriveBrief].self, "drives_last").first
        let lastDetail = try XCTUnwrap(DriveDetail.make(summary: parked, live: nil, last: last))
        try snapshot("detail-last-drive", DriveDetailCard(detail: lastDetail, clock: clock.addingTimeInterval(3600)))
    }

    private func snapshot(_ name: String, _ card: some View) throws {
        for scheme in [ColorScheme.light, .dark] {
            let view = card
                .padding()
                .frame(width: 393)
                .background(Color(.systemGroupedBackground))
                .environment(\.colorScheme, scheme)
            let renderer = ImageRenderer(content: view)
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
}
