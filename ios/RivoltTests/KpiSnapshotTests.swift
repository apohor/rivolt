import SwiftUI
import XCTest
@testable import Rivolt

/// The six tiles under the hero, from the shared fixtures, in light and dark and at an accessibility text size.
@MainActor
final class KpiSnapshotTests: XCTestCase {
    private func content() throws -> some View {
        let a = try Fixture.decode(ActivityResponse.self, "activity")
        let ledger = try Fixture.decode(LedgerMonthSpend.self, "ledger_monthly")
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let points = try Fixture.decode(ChartPoints.self, "chart_points")
        let end = try XCTUnwrap(JSON.parseDate("2026-10-19T04:00:00Z"))
        let tiles = KpiLogic.tiles(drives: KpiLogic.drives(a.drives), charges: KpiLogic.charges(a.charges),
                                   since: KpiLogic.sinceCharge(end: end, points: points), sinceKnown: false,
                                   ledger: ledger, ledgerPending: false, metric: false, calendar: utc)
        let idle = IdleAwakeTile.make(sleep: try Fixture.decode([DayActivity].self, "sleep_activity"),
                                      now: try XCTUnwrap(JSON.parseDate("2026-10-21T15:00:00Z")), calendar: utc)
        return KpiGrid(tiles: tiles, idle: idle, idleLoading: false)
            .padding(16)
            .frame(width: 390)
            .themePage()
    }

    func testRenderTheTiles() throws {
        Theme.registerFonts()
        for (name, size) in [("kpi", DynamicTypeSize.large), ("kpi-ax", .accessibility3)] {
            for scheme in [ColorScheme.light, .dark] {
                let renderer = ImageRenderer(content: try content().environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, size).fixedSize(horizontal: false, vertical: true))
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
}
