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
                                     chargerPowerKw: 0, at: at, update: OtaStep(step: "installing", progress: 37, version: "2026.38.0"))
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

    /// With ios_now_tidy the charging card carries the power and the finish time, so the chip is just the state; other states keep their text.
    func testTidyChipIsJustTheStateWhileTheChargingCardIsShown() {
        let long = "Charging · 149 kW · done by 8:55 AM"
        XCTAssertEqual(NowCard.tidyChip(long, kind: .charging, chargingCardShown: true), "Charging")
        XCTAssertEqual(NowCard.tidyChip(long, kind: .charging, chargingCardShown: false), long, "no card, the chip keeps the figures")
        XCTAssertEqual(NowCard.tidyChip("Driving · 100 km/h", kind: .driving, chargingCardShown: true), "Driving · 100 km/h")
        XCTAssertNil(NowCard.tidyChip(nil, kind: .charging, chargingCardShown: true))
    }

    /// Accessibility text sizes and long chips put the Live button on its own row, so its word is never squeezed.
    func testTidyHeaderStacksAtAccessibilitySizesAndForLongChips() {
        for chip in ["Charging", "Driving · 100 km/h"] {
            XCTAssertFalse(NowCard.tidyHeaderStacked(chip: chip, accessibilitySize: false), chip)
            XCTAssertTrue(NowCard.tidyHeaderStacked(chip: chip, accessibilitySize: true), chip)
        }
        XCTAssertTrue(NowCard.tidyHeaderStacked(chip: "Asleep · since 10:30 PM", accessibilitySize: false))
    }

    /// The tidy hero with the key on, charging and driving, on a 320 pt and a 430 pt phone at the default and the largest accessibility text size:
    /// it renders whole (no zero or runaway height). The header never shows the Live word split or the chip twice (see the two tests above).
    func testRenderTidyAtAccessibilitySizes() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let at = try XCTUnwrap(now.at)
        let clock = at.addingTimeInterval(120)
        let charging = NowSummary(state: st, now: now, clock: clock)
        let driving = NowSummary(input: NowSummary.Input(socPct: 64, rangeKm: 280, chargeTargetPct: 80, gear: "D", powerState: "go", chargerState: "charger_disconnected",
                                                         chargerPowerKw: 0, at: at, speedKph: 100), now: nil, clock: at.addingTimeInterval(15))
        let place = try JSON.decoder().decode(LiveDestination.self, from: Data(#"{"name":"Riverside Community Medical Center and Outpatient Pavilion","lat":10.02,"lon":10.03,"eta":"2027-01-15T09:40:00Z","miles_left":12.4}"#.utf8))
        let cases: [(String, NowSummary, Date)] = [("charging", charging, clock), ("driving", driving, at.addingTimeInterval(15))]
        // The destination block (the map under it needs the app's state, so the block alone): a long name wraps whole beside no clipped figure.
        for (width, size) in [(320.0, DynamicTypeSize.large), (320, .accessibility3), (430, .accessibility3)] {
            let r = ImageRenderer(content: NowDestinationBlock(destination: place).environment(\.dynamicTypeSize, size).padding().frame(width: width))
            r.scale = 2
            let img = try XCTUnwrap(r.uiImage)
            XCTAssertGreaterThan(img.size.height, 40)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty, let png = img.pngData() {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("tidy-destblock-\(Int(width))-\(size.isAccessibilitySize ? "ax3" : "large").png"))
            }
        }
        for (name, summary, when) in cases {
            for (width, size, label) in [(320.0, DynamicTypeSize.large, "se"), (320, .accessibility3, "se-ax3"), (430, .accessibility3, "max-ax3")] {
                let chip = try XCTUnwrap(NowChip.text(for: summary, clock: when) as String?)
                let card = NowCard(vehicle: vehicle, summary: summary, clock: when, v2: true, onLive: {}, slotOn: true, snapshot: now, tidy: true, chip: chip, chipTone: .live)
                    .environment(\.dynamicTypeSize, size)
                let renderer = ImageRenderer(content: card.padding().frame(width: width).background(Color(.systemGroupedBackground)))
                renderer.scale = 2
                let image = try XCTUnwrap(renderer.uiImage, "\(name) \(label) did not render")
                XCTAssertGreaterThan(image.size.height, 150, "\(name) \(label)")
                XCTAssertLessThan(image.size.height, 4000, "\(name) \(label)")
                if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty, let png = image.pngData() {
                    try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                    try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("tidy-\(name)-\(label).png"))
                }
            }
        }
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
