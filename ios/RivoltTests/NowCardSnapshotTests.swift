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

    /// The truck's own time left (a taper: longer than our straight line) is shown plainly, with no "est.".
    func testRenderChargingTrucksOwnEta() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let clock = try XCTUnwrap(now.at).addingTimeInterval(120)
        var input = NowSummary.Input(st)
        input.timeToEndMins = 135
        input.timeToEndValidity = "valid"
        let summary = NowSummary(input: input, now: now, clock: clock)
        XCTAssertEqual(summary.chargeEta, ChargeETA.Result(minutes: 135, source: .truck))
        try snapshot("now-charging-truck-eta", NowCard(vehicle: vehicle, summary: summary, clock: clock))
    }

    func testRenderStale() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let clock = try XCTUnwrap(now.at).addingTimeInterval(3 * 3600)
        try snapshot("now-stale", NowCard(vehicle: vehicle, summary: NowSummary(state: st, now: now, clock: clock), clock: clock))
    }

    func testRenderComfort() throws {
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        func card(_ comfort: NowComfort?, age: TimeInterval) -> NowCard {
            let input = NowSummary.Input(socPct: 72, rangeKm: 330, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_disconnected",
                                         chargerPowerKw: 0, at: at, comfort: comfort)
            return NowCard(vehicle: vehicle, summary: NowSummary(input: input, now: nil, clock: at.addingTimeInterval(age)), clock: at.addingTimeInterval(age))
        }
        let comfort = NowComfort(cabinTempC: 20, preconditioning: nil, petMode: "on", cabinHold: "on")
        try snapshot("now-comfort", card(comfort, age: 15))
        try snapshot("now-comfort-stale", card(comfort, age: 600))
        // Cabin on its own row (whole degrees), with no line under it when the truck says nothing else.
        try snapshot("now-cabin-row", card(NowComfort(cabinTempC: 28.6, preconditioning: nil, petMode: nil, cabinHold: nil), age: 15))
    }

    func testRenderInstalling() throws {
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let input = NowSummary.Input(socPct: 72, rangeKm: 330, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_disconnected",
                                     chargerPowerKw: 0, at: at, update: NowUpdate(status: "installing", progress: 40, current: "2026.34.1", available: "2026.38.0"))
        try snapshot("now-installing", NowCard(vehicle: vehicle, summary: NowSummary(input: input, now: nil, clock: at.addingTimeInterval(15)), clock: at.addingTimeInterval(15)))
    }

    func testRenderCharts() throws {
        let model = ChartsModel(points: try Fixture.decode(ChartPoints.self, "chart_points"),
                                sleep: try Fixture.decode([DayActivity].self, "sleep_activity"))
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let now = try XCTUnwrap(JSON.parseDate("2026-10-21T15:00:00Z"))
        let month = PeriodMonth(containing: now, calendar: utc)
        try snapshot("charts", ChartsSection(model: model, month: month, limit: 80, now: now, calendar: utc))
        try snapshot("charts-empty", ChartsSection(model: ChartsModel(points: ChartPoints(drives: [], charges: []), sleep: []), month: month, now: now, calendar: utc))
    }

    func testRenderMilestoneAndThreeThings() throws {
        let m = try XCTUnwrap(Fixture.decode(MilestonesResponse.self, "milestones").milestones.first)
        try snapshot("milestone", MilestoneCard(milestone: m, failed: false, onHide: {}))
        try snapshot("three-things", ThreeThingsCard(rows: [.push, .lease, .statement], webURL: { _ in URL(string: "https://example.invalid") }, onOpenNotifications: {}, onHide: {}))
    }

    func testRenderPlaces() throws {
        let split = try Fixture.decode(ChargeSplit.self, "charge_split")
        try snapshot("places", PlacesCard(split: split, periodTitle: "Oct 2026"))
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
            XCTAssertGreaterThan(image.size.height, 100)
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
