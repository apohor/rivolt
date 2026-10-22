import SwiftUI
import XCTest
@testable import Rivolt

/// The Activity rows rendered from the shared fixture, light and dark and at an accessibility text size, to PNGs that
/// CI uploads next to the other snapshots. The detail screen is a UIKit-backed List, which ImageRenderer can't draw.
@MainActor
final class ActivitySnapshotTests: XCTestCase {
    private var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }

    private func card(_ r: ActivityResponse) -> some View {
        let calendar = utc
        let days = ActivityLogic.days(r, filter: .all, calendar: calendar)
        return VStack(alignment: .leading, spacing: 0) {
            ForEach(days) { day in
                Text(day.day, format: .dateTime.weekday(.abbreviated).month(.abbreviated).day().locale(Locale(identifier: "en_US")))
                    .font(.footnote.weight(.semibold)).foregroundStyle(NowInk.secondary).padding(.top, 10).padding(.bottom, 2)
                ForEach(day.items) { item in
                    switch item {
                    case .drive(let d): ActivityDriveRow(drive: d, calendar: calendar)
                    case .charge(let c): ActivityChargeRow(charge: c, calendar: calendar)
                    }
                    Divider()
                }
            }
        }
        .padding(16)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
    }

    func testRenderActivity() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        try render("activity", card(r), size: nil)
        try render("activity-ax", card(r), size: .accessibility3)
    }

    private func render(_ name: String, _ content: some View, size: DynamicTypeSize?) throws {
        for scheme in [ColorScheme.light, .dark] {
            let view = content
                .padding()
                .frame(width: 393)
                .fixedSize(horizontal: false, vertical: true)
                .background(Color(.systemGroupedBackground))
                .environment(\.colorScheme, scheme)
                .environment(\.dynamicTypeSize, size ?? .large)
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
