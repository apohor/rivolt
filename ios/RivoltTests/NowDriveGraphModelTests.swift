import XCTest
@testable import Rivolt

/// The Now card's driving graph numbers (design_preview key now_drive_graph), as the web's DriveGraph: a smoothed summary line, the top speed from a real
/// reading, the change against the ring, and nothing at all for a drive too short to draw.
@MainActor
final class NowDriveGraphModelTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func sample(_ s: Double, mph: Double, pct: Double?) -> DriveSample {
        DriveSample(at: t0.addingTimeInterval(s), lat: 40, lon: -100, speedMph: mph, shiftState: "D", locationFixAt: nil, batteryLevelPct: pct)
    }

    /// A drive of `minutes`, a reading every 10 s: 50 mph with one 80 mph spike at the 8th minute, the battery falling 0.1 % a minute from 70.
    private func drive(minutes: Double) -> [DriveSample] {
        stride(from: 0.0, through: minutes * 60, by: 10).map { s in
            sample(s, mph: abs(s - 480) < 1 ? 80 : 50, pct: 70 - s / 60 * 0.1)
        }
    }

    private func make(_ rows: [DriveSample], nowPct: Double? = 68, metric: Bool = false) -> NowDriveGraphModel? {
        NowDriveGraphModel.make(samples: rows, startedAt: t0, nowPct: nowPct, metric: metric, time: { _ in "11:38 AM" })
    }

    func testTooShortIsNothing() {
        XCTAssertNil(make(drive(minutes: 1)))
        XCTAssertNil(make([]))
        XCTAssertNil(make(Array(drive(minutes: 20).prefix(3))), "three readings are not a drive")
    }

    func testTheLineIsAtMostThirtySixEvenPoints() throws {
        let m = try XCTUnwrap(make(drive(minutes: 22)))
        XCTAssertEqual(m.speed.count, NowDriveGraphModel.seriesPoints)
        XCTAssertEqual(m.speed.first?.x, 0)
        XCTAssertEqual(m.speed.last?.x ?? 0, 1, accuracy: 1e-9)
        let gaps = zip(m.speed, m.speed.dropFirst()).map { $1.x - $0.x }
        XCTAssertEqual(gaps.max() ?? 0, gaps.min() ?? 0, accuracy: 1e-9, "evenly spaced in time")
    }

    func testTheTopIsARealReadingNotTheSmoothedCurve() throws {
        let m = try XCTUnwrap(make(drive(minutes: 22)))
        XCTAssertEqual(m.top?.label, "80 mph")
        // The smoothed line never reaches the spike: the label does.
        XCTAssertLessThan(m.speed.map(\.y).max() ?? 1, m.top?.pt.y ?? 0)
    }

    func testKilometresPerHour() throws {
        let m = try XCTUnwrap(make(drive(minutes: 22), metric: true))
        XCTAssertEqual(m.speedUnit, "km/h")
        XCTAssertEqual(m.top?.label, "129 km/h")
    }

    func testTheChangeIsTheFirstReadingAgainstTheRing() throws {
        let m = try XCTUnwrap(make(drive(minutes: 22), nowPct: 66))
        XCTAssertEqual(m.change, "−4%")
        XCTAssertEqual(m.since, "since 11:38 AM")
        XCTAssertNil(try XCTUnwrap(make(drive(minutes: 22), nowPct: 70)).change, "no change to say")
        XCTAssertNil(try XCTUnwrap(make(drive(minutes: 22), nowPct: nil)).change)
    }

    func testNoBatteryReadingsLeavesOnlyTheSpeed() throws {
        let rows = drive(minutes: 22).map { sample($0.at.timeIntervalSince(t0), mph: $0.speedMph ?? 0, pct: nil) }
        let m = try XCTUnwrap(make(rows))
        XCTAssertTrue(m.soc.isEmpty)
        XCTAssertNil(m.change)
    }

    func testAStopStaysOnTheAxis() throws {
        let rows = stride(from: 0.0, through: 1080, by: 10).map { s in sample(s, mph: s >= 360 && s < 540 ? 0 : 40, pct: 64) }
        let m = try XCTUnwrap(make(rows))
        XCTAssertTrue(m.speed.allSatisfy { $0.y >= 0 && $0.y <= 1 })
        XCTAssertTrue(m.speed.contains { $0.y < 0.05 })
    }

    func testTheDestinationLine() throws {
        let d = try JSON.decoder().decode(LiveDestination.self, from: Data(#"{"name":"Work","lat":40.3,"lon":-100.4,"eta":"2026-11-20T12:05:00Z","miles_left":12.4}"#.utf8))
        XCTAssertEqual(NowDriveGraphModel.destinationLine(d, metric: false, arrive: { _ in "12:05 PM" }), "12 mi to Work · arrive 12:05 PM")
        XCTAssertEqual(NowDriveGraphModel.destinationLine(d, metric: true, arrive: { _ in "12:05 PM" }), "20 km to Work · arrive 12:05 PM")
    }

    private struct Source: DriveSamplesSource {
        let rows: [DriveSample]
        var fails = false
        func driveSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [DriveSample] {
            if fails { throw APIError.decoding(NSError(domain: "t", code: 1)) }
            return rows
        }
    }

    func testTheSeriesModelKeepsOneDriveAtATime() async {
        let model = NowDriveSeriesModel()
        XCTAssertFalse(model.matches(vehicleID: "a", startedAt: t0))
        await model.load(source: Source(rows: drive(minutes: 5)), vehicleID: "a", startedAt: t0)
        XCTAssertEqual(model.samples.count, drive(minutes: 5).count)
        XCTAssertTrue(model.matches(vehicleID: "a", startedAt: t0.addingTimeInterval(20)), "the start wanders by seconds between reads")
        XCTAssertFalse(model.matches(vehicleID: "b", startedAt: t0))
        XCTAssertFalse(model.matches(vehicleID: "a", startedAt: t0.addingTimeInterval(3600)), "another drive")
        // A failed read keeps the rows of the same drive; another drive's start over empty.
        await model.load(source: Source(rows: [], fails: true), vehicleID: "a", startedAt: t0)
        XCTAssertFalse(model.samples.isEmpty)
        await model.load(source: Source(rows: [], fails: true), vehicleID: "a", startedAt: t0.addingTimeInterval(3600))
        XCTAssertTrue(model.samples.isEmpty)
    }

    /// The drive's start is the server's own started_at, not now minus the elapsed time (that is measured to the last frame).
    func testLiveDriveCarriesTheServersStart() throws {
        let d = try JSON.decoder().decode(LiveDrive.self, from: Data(#"{"started_at":"2026-11-20T11:38:00Z","elapsed_sec":1320,"distance_mi":10}"#.utf8))
        XCTAssertEqual(d.startedAt, ISO8601DateFormatter().date(from: "2026-11-20T11:38:00Z"))
        let old = try JSON.decoder().decode(LiveDrive.self, from: Data(#"{"elapsed_sec":60}"#.utf8))
        XCTAssertNil(old.startedAt)
    }
}
