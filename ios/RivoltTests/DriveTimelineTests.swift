import SwiftUI
import XCTest
@testable import Rivolt

/// The drive screen's one read (row and samples, shared by the map and the timeline), the timeline's figures, and
/// renders of the card for review (light, dark, an accessibility text size, no samples).
@MainActor
final class DriveTimelineTests: XCTestCase {
    private func row() throws -> ActivityDrive {
        try XCTUnwrap(Fixture.decode(ActivityResponse.self, "activity").drives.first { $0.id == "drv-3" })
    }

    /// A synthetic 65-minute drive in 10 s steps: city, highway, a hard stop, a mode change, the battery falling.
    private func samples(for d: ActivityDrive) -> [DriveSample] {
        let n = Int(d.endedAt.timeIntervalSince(d.startedAt) / 10)
        return (0...n).map { i in
            let x = Double(i)
            let t: Double = x / Double(n)
            var speed: Double = 20
            if t < 0.2 {
                speed = 25 + 10 * sin(x / 3)
            } else if t < 0.7 {
                speed = 62 + 4 * sin(x / 5)
            } else if t >= 0.72 {
                speed = 30 + 8 * sin(x / 4)
            }
            let at: Date = d.startedAt.addingTimeInterval(x * 10)
            let battery: Double = 85 - 5 * t
            let altitude: Double = 150 + 40 * sin(t * 6)
            let mode: String = t < 0.5 ? "everyday" : "sport"
            return DriveSample(at: at, lat: 32.7767 + t * 0.2, lon: -96.797, speedMph: speed, shiftState: "D", locationFixAt: nil,
                               batteryLevelPct: battery, altitudeM: altitude, driveMode: mode)
        }
    }

    private final class Fake: DriveDetailSource, @unchecked Sendable {
        let route: DriveRoute
        let samples: [DriveSample]
        var failRoute = false
        private(set) var sampleReads: [(Date, Date, String)] = []
        init(route: DriveRoute, samples: [DriveSample]) {
            self.route = route
            self.samples = samples
        }
        func driveRoute(id: String) async throws -> DriveRoute {
            if failRoute { throw APIError.server(status: 404, message: "no") }
            return route
        }
        func driveSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [DriveSample] {
            sampleReads.append((since, until, vehicleID))
            return samples
        }
    }

    private func route(_ d: ActivityDrive) throws -> DriveRoute {
        let f = ISO8601DateFormatter()
        let json = #"{"ID":"\#(d.id)","VehicleID":"veh-fixture","StartedAt":"\#(f.string(from: d.startedAt))","EndedAt":"\#(f.string(from: d.endedAt))","StartLat":32.7767,"StartLon":-96.797,"EndLat":32.9767,"EndLon":-96.797}"#
        return try JSON.decoder().decode(DriveRoute.self, from: Data(json.utf8))
    }

    func testOneReadFeedsTheMapAndTheTimeline() async throws {
        let d = try row()
        let src = Fake(route: try route(d), samples: samples(for: d))
        let model = DriveDetailModel()
        await model.load(source: src, drive: d)
        XCTAssertEqual(src.sampleReads.count, 1, "one /api/samples read for the screen")
        XCTAssertEqual(src.sampleReads[0].0, d.startedAt.addingTimeInterval(-600))
        XCTAssertEqual(src.sampleReads[0].1, d.endedAt.addingTimeInterval(600))
        guard case .loaded(_, let s) = model.phase else { return XCTFail("\(model.phase)") }
        XCTAssertEqual(s?.count, src.samples.count)
        let t = try XCTUnwrap(model.timeline)
        XCTAssertEqual(t.distinctModes, [.allPurpose, .sport])
        XCTAssertTrue(t.moments.contains { $0.kind == .highwayEntry })
        XCTAssertTrue(t.moments.contains { $0.kind == .topSpeed })
        XCTAssertGreaterThan(t.drawKwh, 0)
        XCTAssertLessThanOrEqual(t.regenKwh, t.drawKwh)
        XCTAssertEqual(t.regenTile.label, "Regen recovered")
        XCTAssertEqual(t.regenTile.unit, "kWh")
        // A failed row: the map says unavailable, the timeline couldn't load.
        src.failRoute = true
        await model.load(source: src, drive: d)
        XCTAssertEqual(model.phase, .failed)
        XCTAssertNil(model.timeline)
    }

    func testTheTimelineNeedsSamplesInsideTheDrive() throws {
        let d = try row()
        XCTAssertNil(DriveTimelineData(drive: d, samples: []))
        let outside = samples(for: d).map { s in
            DriveSample(at: s.at.addingTimeInterval(-10_000), lat: s.lat, lon: s.lon, speedMph: s.speedMph, shiftState: s.shiftState, locationFixAt: nil)
        }
        XCTAssertNil(DriveTimelineData(drive: d, samples: outside))
    }

    // MARK: Renders for review (SNAPSHOT_DIR)

    private func card(_ phase: DriveDetailModel.Phase, _ data: DriveTimelineData?, _ d: ActivityDrive) -> some View {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        return DriveTimelineCard(drive: d, phase: phase, data: data, calendar: utc, metric: false)
            .padding(16)
            .frame(width: 390, alignment: .leading)
            .themePage()
    }

    func testRenderTheTimeline() throws {
        Theme.registerFonts()
        let d = try row()
        let s = samples(for: d)
        let data = try XCTUnwrap(DriveTimelineData(drive: d, samples: s))
        let phase = DriveDetailModel.Phase.loaded(try route(d), samples: s)
        let renders: [(String, AnyView)] = [
            ("drive-timeline-light", AnyView(card(phase, data, d).environment(\.colorScheme, .light))),
            ("drive-timeline-dark", AnyView(card(phase, data, d).environment(\.colorScheme, .dark))),
            ("drive-timeline-accessibility-light", AnyView(card(phase, data, d).environment(\.colorScheme, .light).environment(\.dynamicTypeSize, .accessibility3))),
            ("drive-timeline-empty-dark", AnyView(card(.loaded(try route(d), samples: []), nil, d).environment(\.colorScheme, .dark))),
        ]
        for (name, content) in renders {
            let renderer = ImageRenderer(content: content.fixedSize(horizontal: false, vertical: true))
            renderer.scale = name.contains("accessibility") ? 1.5 : 3
            let image = try XCTUnwrap(renderer.uiImage, "\(name) did not render")
            XCTAssertGreaterThan(image.size.height, 80, name)
            let png = try XCTUnwrap(image.pngData(), name)
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = "\(name).png"
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
            }
        }
    }
}
