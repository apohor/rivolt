import SwiftUI
import XCTest
@testable import Rivolt

/// The Activity feed (day headers, the timeline rail, drive and charge cards, the filter) and the detail tiles, from the
/// shared fixture, next to qa's web screenshots. The screens themselves hold a NavigationStack, which ImageRenderer
/// can't draw, so these are their parts.
@MainActor
final class ActivityFeedSnapshotTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private func feed() throws -> some View {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let calendar = utc
        let days = ActivityLogic.days(r, filter: .all, calendar: calendar)
        return VStack(alignment: .leading, spacing: 16) {
            ThemeSegmented(options: ActivityFilter.allCases.map { ThemeSegmented<ActivityFilter>.Option(id: $0, label: $0.label) }, selection: .constant(.all))
            ForEach(days) { day in
                VStack(alignment: .leading, spacing: 12) {
                    ActivityDayHeader(day: day.day, calendar: calendar)
                    VStack(spacing: 0) {
                        ForEach(day.items) { ActivityFeedRow(item: $0, calendar: calendar) }
                    }
                }
            }
        }
        .padding(16)
        .frame(width: 390, alignment: .leading)
        .themePage()
    }

    private func detail() throws -> some View {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let drive = try XCTUnwrap(r.drives.first { $0.id == "drv-3" })
        let charge = try XCTUnwrap(r.charges.first { $0.id == "chg-fast-1" })
        let columns = [GridItem(.flexible(), spacing: 12, alignment: .top), GridItem(.flexible(), spacing: 12, alignment: .top)]
        return VStack(alignment: .leading, spacing: 16) {
            Text("Drive").themeText(.hero)
            LazyVGrid(columns: columns, spacing: 12) { ForEach(ActivityLogic.tiles(drive, metric: false)) { KpiTileView(tile: $0) } }
            Text("Fast charge").themeText(.hero)
            LazyVGrid(columns: columns, spacing: 12) { ForEach(ActivityLogic.tiles(charge)) { KpiTileView(tile: $0) } }
        }
        .padding(16)
        .frame(width: 390, alignment: .leading)
        .themePage()
    }

    func testRenderTheFeedAndTheDetail() throws {
        Theme.registerFonts()
        for (name, content) in [("activity-feed", AnyView(try feed())), ("activity-detail", AnyView(try detail()))] {
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
    }
}
