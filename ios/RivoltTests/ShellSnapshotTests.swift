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

    private let at = Date(timeIntervalSince1970: 1_800_000_000)

    /// One of the web's Now states as a summary: `age` is how old the newest frame is.
    private func summary(soc: Double, gear: String = "P", power: String? = nil, kw: Double? = nil, sleep: Bool = false,
                         kph: Double? = nil, target: Double? = 80, age: TimeInterval = 20, peak: Double? = nil) -> NowSummary {
        let input = NowSummary.Input(
            socPct: soc, rangeKm: soc * 3.0, chargeTargetPct: target, gear: gear, powerState: sleep ? "sleep" : "ready",
            chargerState: power, chargerPowerKw: kw, at: at.addingTimeInterval(-age), packKwh: 131, speedKph: kph)
        return NowSummary(input: input, now: nil, clock: at)
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

    /// The web's eleven Now states (qa's clean set), each as the hero and any warning under it.
    func testRenderTheNowStates() throws {
        let v = try vehicle()
        func state(_ name: String, _ s: NowSummary) throws {
            let alerts = NowCard.alerts(for: s, vehicleName: v.name)
            try render("shell-now-\(name)", page(VStack(spacing: 16) {
                NowCard(vehicle: v, summary: s, clock: at)
                ForEach(alerts) { NowAlertBanner(alert: $0) }
            }))
        }
        try state("asleep", summary(soc: 67, sleep: true, age: 60))
        try state("awake-parked", summary(soc: 67))
        try state("charge-complete", summary(soc: 80, power: "charger_connected", kw: 0))
        try state("charging-fast", summary(soc: 31, power: "charging_active", kw: 142, peak: 150))
        try state("charging-home", summary(soc: 52, power: "charging_active", kw: 7.2))
        try state("driving", summary(soc: 58, gear: "D", kph: 88))
        try state("low-battery", summary(soc: 7))
        try state("offline", summary(soc: 67, age: 47 * 3600))
        try state("plugged-idle", summary(soc: 55, power: "charger_connected", kw: 0))
        try state("stale", summary(soc: 41, age: 5 * 60))
    }

    /// The whole Now page in the order HomeView lays it out, to compare with the web's full-page capture.
    func testRenderTheWholeNowPage() throws {
        let v = try vehicle()
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let now = try XCTUnwrap(JSON.parseDate("2026-10-21T15:00:00Z"))
        let month = PeriodMonth(containing: now, calendar: utc)
        let a = try Fixture.decode(ActivityResponse.self, "activity")
        let tiles = KpiLogic.tiles(drives: KpiLogic.drives(a.drives), charges: KpiLogic.charges(a.charges), since: nil, sinceKnown: true,
                                   ledger: try Fixture.decode(LedgerMonthSpend.self, "ledger_monthly"), ledgerPending: false, metric: false, calendar: utc)
        let idle = IdleAwakeTile.make(sleep: try Fixture.decode([DayActivity].self, "sleep_activity"), now: now, calendar: utc)
        let charts = ChartsModel(points: try Fixture.decode(ChartPoints.self, "chart_points"), sleep: try Fixture.decode([DayActivity].self, "sleep_activity"))
        let split = try Fixture.decode(ChargeSplit.self, "charge_split")
        let s = summary(soc: 52, power: "charging_active", kw: 7.2)
        let whole = VStack(spacing: 0) {
            ThemeTopBar { Image(systemName: "person.crop.circle").font(.title3).foregroundStyle(Theme.textSecondary) }
            VStack(spacing: 16) {
                ThreeThingsCard(rows: [.lease], webURL: { _ in URL(string: "https://example.invalid") }, onOpenNotifications: {}, onHide: {})
                HStack(alignment: .center) {
                    Text("Now").themeText(.hero)
                    Spacer()
                    ThemeStepper(title: month.title(calendar: utc), canGoForward: false, previous: {}, next: {})
                }
                NowCard(vehicle: v, summary: s, clock: at, place: "Home", session: try Fixture.decode(LiveSession.self, "live_session_home"))
                KpiGrid(tiles: tiles, idle: idle, idleLoading: false)
                PlacesCard(split: split, periodTitle: month.title(calendar: utc))
                ChartsSection(model: charts, month: month, limit: 80, now: now, calendar: utc)
            }
            .padding(16)
            ThemeTabBar(items: tabs, selection: .constant("now"))
        }
        .frame(width: 390)
        .background(ThemePageBackground())
        try render("shell-now-page", whole)
    }
}
