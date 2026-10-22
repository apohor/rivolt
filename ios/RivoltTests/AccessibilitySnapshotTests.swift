import SwiftUI
import XCTest
@testable import Rivolt

/// The screens at accessibility text sizes, light and dark, to check by eye that nothing clips or overlaps and the grids
/// collapse to one column: Now, the Activity feed and detail, and Money. (The tab screens themselves hold a
/// NavigationStack, which ImageRenderer can't draw, so these are their parts.)
@MainActor
final class AccessibilitySnapshotTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private let at = Date(timeIntervalSince1970: 1_800_000_000)

    private func render(_ name: String, _ content: some View, size: DynamicTypeSize) throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: content.environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, size).fixedSize(horizontal: false, vertical: true))
            renderer.scale = 1  // whole pages at these sizes are thousands of points tall
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

    private enum Part { case top, bottom }

    private func nowPage(part: Part) throws -> some View {
        let v = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let now = try XCTUnwrap(JSON.parseDate("2026-10-21T15:00:00Z"))
        let month = PeriodMonth(containing: now, calendar: utc)
        let a = try Fixture.decode(ActivityResponse.self, "activity")
        let tiles = KpiLogic.tiles(drives: KpiLogic.drives(a.drives), charges: KpiLogic.charges(a.charges), since: nil, sinceKnown: true,
                                   ledger: try Fixture.decode(LedgerMonthSpend.self, "ledger_monthly"), ledgerPending: false, metric: false, calendar: utc)
        let sleep = try Fixture.decode([DayActivity].self, "sleep_activity")
        let charts = ChartsModel(points: try Fixture.decode(ChartPoints.self, "chart_points"), sleep: sleep)
        let input = NowSummary.Input(socPct: 52, rangeKm: 325, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charging_active",
                                     chargerPowerKw: 7.2, at: at.addingTimeInterval(-20), packKwh: 131)
        let summary = NowSummary(input: input, now: nil, clock: at)
        let live = try Fixture.decode(LiveSession.self, "live_session_home")
        let split = try Fixture.decode(ChargeSplit.self, "charge_split")
        let server = try XCTUnwrap(URL(string: "https://rivolt.dev"))
        return VStack(spacing: 0) {
            ThemeTopBar { Image(systemName: "person.crop.circle").font(.title3).foregroundStyle(Theme.textSecondary) }
            VStack(spacing: 16) {
                if part == .top {
                    ThreeThingsCard(rows: [.push, .lease, .statement], webURL: { _ in server }, onOpenNotifications: {}, onHide: {})
                    HStack(alignment: .center) {
                        Text("Now").themeText(.hero)
                        Spacer()
                        ThemeStepper(title: month.title(calendar: utc), spokenTitle: month.spokenTitle(calendar: utc), canGoForward: false, previous: {}, next: {})
                    }
                    NowCard(vehicle: v, summary: summary, clock: at, place: "Home", session: live)
                    KpiGrid(tiles: tiles, idle: IdleAwakeTile.make(sleep: sleep, now: now, calendar: utc), idleLoading: false)
                } else {
                    PlacesCard(split: split, periodTitle: month.title(calendar: utc))
                    ChartsSection(model: charts, month: month, limit: 80, now: now, calendar: utc)
                    PageFooter(server: server)
                }
            }
            .padding(16)
        }
        .frame(width: 390)
        .background(ThemePageBackground())
    }

    func testNowAtAccessibilitySizes() throws {
        // The page is split in two: at these sizes it is taller than one image can be.
        try render("a11y-now-top-ax3", nowPage(part: .top), size: .accessibility3)
        try render("a11y-now-bottom-ax3", nowPage(part: .bottom), size: .accessibility3)
        try render("a11y-now-top-ax5", nowPage(part: .top), size: .accessibility5)
    }

    func testActivityAtAnAccessibilitySize() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let cal = utc
        let days = ActivityLogic.days(r, filter: .all, calendar: cal)
        let feed = VStack(alignment: .leading, spacing: 16) {
            ThemeSegmented(options: ActivityFilter.allCases.map { ThemeSegmented<ActivityFilter>.Option(id: $0, label: $0.label) }, selection: .constant(.all))
            ForEach(days.prefix(4)) { day in
                VStack(alignment: .leading, spacing: 12) {
                    ActivityDayHeader(day: day.day, calendar: cal)
                    VStack(spacing: 0) { ForEach(day.items) { ActivityFeedRow(item: $0, calendar: cal) } }
                }
            }
        }
        .padding(16).frame(width: 390, alignment: .leading).themePage()
        try render("a11y-activity-ax3", feed, size: .accessibility3)
        let drive = try XCTUnwrap(r.drives.first { $0.id == "drv-3" })
        let detail = VStack(alignment: .leading, spacing: 16) {
            Text("Drive").themeText(.hero)
            VStack(spacing: 12) { ForEach(ActivityLogic.tiles(drive, metric: false)) { KpiTileView(tile: $0) } }
        }
        .padding(16).frame(width: 390, alignment: .leading).themePage()
        try render("a11y-activity-detail-ax3", detail, size: .accessibility3)
    }

    func testMoneyAtAnAccessibilitySize() throws {
        let model = MoneyModel(month: try Fixture.decode(LedgerMonth.self, "ledger_monthly"),
                               summary: try Fixture.decode(LedgerSummary.self, "ledger_summary"),
                               savings: try Fixture.decode(LedgerSavings.self, "ledger_savings"))
        let cal = utc
        let now = try XCTUnwrap(JSON.parseDate("2026-10-21T17:00:00Z"))
        let page = VStack(spacing: 0) {
            ThemeTopBar { EmptyView() }
            HStack {
                Text("Money").themeText(.hero)
                Spacer()
                ThemeStepper(title: "Sep 2026", canGoForward: false, previous: {}, next: {})
            }
            .padding(.horizontal, 16).padding(.vertical, 8)
            MoneyContent(model: model, filter: .constant(.all), metric: .constant(.cost), savingsWhich: .constant(.last),
                         noticeHidden: .constant(false), serverURL: URL(string: "https://rivolt.example"), now: now, calendar: cal,
                         usesMetric: false, export: .init(share: {}, csv: {}))
                .padding(16)
        }
        .frame(width: 390)
        .background(ThemePageBackground())
        try render("a11y-money-ax3", page, size: .accessibility3)
    }

    func testTruckAtAnAccessibilitySize() throws {
        let m = TruckModel(state: try Fixture.decode(VehicleState.self, "state"),
                           drain: .value(try Fixture.decode(DrainSnapshot.self, "drain")),
                           pack: .value(try Fixture.decode(PackHealth.self, "pack_health")),
                           trends: .value(try Fixture.decode(TruckTrends.self, "trends")),
                           cold: .value(try Fixture.decode(ColdRange.self, "cold_range")),
                           lease: .value(try Fixture.decode(LeaseStatus.self, "lease_status")),
                           leaseTerms: try Fixture.decode(LeaseTerms.self, "lease_terms"),
                           sleep: .value(try Fixture.decode([DayActivity].self, "sleep_activity")))
        let v = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let now = try XCTUnwrap(JSON.parseDate("2026-10-22T17:00:00Z"))
        let page = VStack(spacing: 0) {
            ThemeTopBar { EmptyView() }
            HStack {
                Text("Truck").themeText(.hero)
                Spacer()
                ThemeStepper(title: "Oct 2026", canGoForward: false, previous: {}, next: {})
            }
            .padding(.horizontal, 16).padding(.vertical, 8)
            TruckContent(model: m, vehicle: v, drainKey: .constant(.week), temp: 15.6, pickTemp: { _ in }, r2Dismissed: .constant(false),
                         month: PeriodMonth(containing: now, calendar: utc), now: now, calendar: utc, metric: false)
                .padding(16)
        }
        .frame(width: 390)
        .background(ThemePageBackground())
        try render("a11y-truck-ax3", page, size: .accessibility3)
    }
}
