import SwiftUI
import XCTest
@testable import Rivolt

/// The scroll audit: each tab's first screen, as one phone screen shows it, on the smallest and the largest iPhone at the
/// default, the largest standard and an accessibility text size. The picture is the screen: the status bar's room, the
/// pinned top bar and title row, the page clipped to the room left, then the tab bar and the home indicator's room.
/// Whatever is missing from a picture is below the fold. (The tabs hold a NavigationStack, which ImageRenderer can't
/// draw, so the pages are put together from their parts in the tabs' own order, as AccessibilitySnapshotTests does.)
@MainActor
final class FoldAuditSnapshotTests: XCTestCase {
    private struct Device {
        let name: String
        let width: CGFloat
        let height: CGFloat
        /// The status bar (and Dynamic Island) above the page; the home indicator below the tab bar.
        let top: CGFloat
        let bottom: CGFloat
    }

    private let devices = [
        Device(name: "se", width: 375, height: 667, top: 20, bottom: 0),
        Device(name: "promax", width: 440, height: 956, top: 62, bottom: 34),
    ]

    private let sizes: [(String, DynamicTypeSize)] = [("default", .large), ("xxxl", .xxxLarge), ("ax3", .accessibility3)]

    private let tabs = [
        ThemeTabItem(id: "now", label: "Now", icon: "now"), ThemeTabItem(id: "activity", label: "Activity", icon: "activity"),
        ThemeTabItem(id: "money", label: "Money", icon: "money"), ThemeTabItem(id: "plan", label: "Plan", icon: "plan"),
        ThemeTabItem(id: "truck", label: "Truck", icon: "truck"),
    ]

    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private let at = Date(timeIntervalSince1970: 1_800_000_000)

    /// The pinned title row of every tab: the page name and the month stepper.
    private func titleRow(_ title: String, _ month: String) -> some View {
        HStack(alignment: .center, spacing: 8) {
            Text(title).themeText(.hero)
            Spacer(minLength: 8)
            ThemeStepper(title: month, canGoForward: false, previous: {}, next: {})
        }
        .padding(.horizontal, 4 * Theme.unit)
        .padding(.vertical, 2 * Theme.unit)
        .background(Theme.background)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.border).frame(height: 1) }
    }

    /// One phone screen: the page under its pinned header, cut where the tab bar starts.
    private func screen(_ d: Device, tab: String, title: String, month: String, @ViewBuilder page: () -> some View) -> some View {
        VStack(spacing: 0) {
            Color.clear.frame(height: d.top)
            ThemeTopBar { Image(systemName: "person.crop.circle").font(.title3).foregroundStyle(Theme.textSecondary) }
            titleRow(title, month)
            page()
                .frame(width: d.width, alignment: .top)
                .frame(maxHeight: .infinity, alignment: .top)
                .clipped()
            ThemeTabBar(items: tabs, selection: .constant(tab))
            Color.clear.frame(height: d.bottom)
        }
        .frame(width: d.width, height: d.height)
        .background(ThemePageBackground())
    }

    private func render(_ name: String, _ content: some View, size: DynamicTypeSize) throws {
        Theme.registerFonts()
        let renderer = ImageRenderer(content: content.environment(\.colorScheme, .dark).environment(\.dynamicTypeSize, size))
        renderer.scale = 2
        let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
        let png = try XCTUnwrap(image.pngData())
        let file = "fold-\(name).png"
        let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        attachment.name = file
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
            try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
        }
    }

    private func eachScreen(_ tab: String, _ build: (Device) throws -> AnyView) throws {
        for d in devices {
            for (label, size) in sizes {
                try render("\(tab)-\(d.name)-\(label)", try build(d), size: size)
            }
        }
    }

    // MARK: Now: battery, range, plugged in or charging (with its ETA) and the status

    private func nowPage(threeThings: Bool) throws -> some View {
        let v = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let now = try XCTUnwrap(JSON.parseDate("2026-10-21T15:00:00Z"))
        let month = PeriodMonth(containing: now, calendar: utc)
        let a = try Fixture.decode(ActivityResponse.self, "activity")
        let tiles = KpiLogic.tiles(drives: KpiLogic.drives(a.drives), charges: KpiLogic.charges(a.charges), since: nil, sinceKnown: true,
                                   ledger: try Fixture.decode(LedgerMonthSpend.self, "ledger_monthly"), ledgerPending: false, metric: false, calendar: utc)
        let sleep = try Fixture.decode([DayActivity].self, "sleep_activity")
        let input = NowSummary.Input(socPct: 52, rangeKm: 325, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charging_active",
                                     chargerPowerKw: 7.2, at: at.addingTimeInterval(-20), packKwh: 131)
        let summary = NowSummary(input: input, now: nil, clock: at)
        let live = try Fixture.decode(LiveSession.self, "live_session_home")
        let split = try Fixture.decode(ChargeSplit.self, "charge_split")
        let server = try XCTUnwrap(URL(string: "https://rivolt.dev"))
        return VStack(spacing: 16) {
            if threeThings {
                ThreeThingsCard(rows: [.push, .lease, .statement], webURL: { _ in server }, onOpenNotifications: {}, onHide: {})
            }
            NowCard(vehicle: v, summary: summary, clock: at, place: "Home", session: live)
            ForEach(NowCard.alerts(for: summary, vehicleName: v.name)) { NowAlertBanner(alert: $0) }
            KpiGrid(tiles: tiles, idle: IdleAwakeTile.make(sleep: sleep, now: now, calendar: utc), idleLoading: false)
            PlacesCard(split: split, periodTitle: month.title(calendar: utc))
        }
        .padding(16)
    }

    func testNowFirstScreen() throws {
        try eachScreen("now") { d in AnyView(screen(d, tab: "now", title: "Now", month: "Oct 2026") { try? nowPage(threeThings: false) }) }
        // An account with history that hasn't set up notifications, its lease or the statement: the card comes first.
        try eachScreen("now-threethings") { d in AnyView(screen(d, tab: "now", title: "Now", month: "Oct 2026") { try? nowPage(threeThings: true) }) }
    }

    // MARK: Activity: the latest drives and charges

    func testActivityFirstScreen() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let cal = utc
        let days = ActivityLogic.days(r, filter: .all, calendar: cal)
        try eachScreen("activity") { d in
            AnyView(screen(d, tab: "activity", title: "Activity", month: "Oct 2026") {
                VStack(alignment: .leading, spacing: 16) {
                    VStack(alignment: .leading, spacing: 2 * Theme.unit) {
                        ThemeSegmented(options: ActivityMode.allCases.map { ThemeSegmented<ActivityMode>.Option(id: $0, label: $0.label) },
                                       selection: .constant(.list), accessibilityName: "View", fillsWidth: true)
                        ThemeSegmented(options: ActivityFilter.allCases.map { ThemeSegmented<ActivityFilter>.Option(id: $0, label: $0.label) },
                                       selection: .constant(.all))
                    }
                    ForEach(days.prefix(4)) { day in
                        VStack(alignment: .leading, spacing: 12) {
                            ActivityDayHeader(day: day.day, calendar: cal)
                            VStack(spacing: 0) { ForEach(day.items) { ActivityFeedRow(item: $0, calendar: cal) } }
                        }
                    }
                }
                .padding(16)
            })
        }
    }

    // MARK: Money: the month's total

    func testMoneyFirstScreen() throws {
        let model = MoneyModel(month: try Fixture.decode(LedgerMonth.self, "ledger_monthly"),
                               summary: try Fixture.decode(LedgerSummary.self, "ledger_summary"),
                               savings: try Fixture.decode(LedgerSavings.self, "ledger_savings"))
        let cal = utc
        let now = try XCTUnwrap(JSON.parseDate("2026-10-21T17:00:00Z"))
        try eachScreen("money") { d in
            AnyView(screen(d, tab: "money", title: "Money", month: "Sep 2026") {
                MoneyContent(model: model, filter: .constant(.all), metric: .constant(.cost), savingsWhich: .constant(.last),
                             noticeHidden: .constant(false), serverURL: URL(string: "https://rivolt.example"), now: now, calendar: cal,
                             usesMetric: false, export: .init(share: {}, csv: {}))
                    .padding(16)
            })
        }
    }

    // MARK: Truck: the health headline

    func testTruckFirstScreen() throws {
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
        let cal = utc
        try eachScreen("truck") { d in
            AnyView(screen(d, tab: "truck", title: "Truck", month: "Oct 2026") {
                TruckContent(model: m, vehicle: v, drainKey: .constant(.week), temp: 15.6, pickTemp: { _ in }, r2Dismissed: .constant(false),
                             month: PeriodMonth(containing: now, calendar: cal), now: now, calendar: cal, metric: false)
                    .padding(16)
            })
        }
    }
}
