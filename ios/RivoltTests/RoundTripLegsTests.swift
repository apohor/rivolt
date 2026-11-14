import XCTest
@testable import Rivolt

/// A merged round trip (three real legs, a zero-distance stub left out by the server) opens with every leg drawn and listed, not the
/// first leg only. The row's shape is backend's `legs[]`; the fixture has made-up places.
@MainActor
final class RoundTripLegsTests: XCTestCase {
    private func row() throws -> ActivityDrive {
        try XCTUnwrap(Fixture.decode(ActivityResponse.self, "activity_round_trip").drives.first)
    }

    private final class Fake: DriveDetailSource, @unchecked Sendable {
        // The legs are read concurrently.
        private let lock = NSLock()
        private var _routeReads: [String] = []
        private var _sampleReads: [(Date, Date)] = []
        var routeReads: [String] { lock.withLock { _routeReads } }
        var sampleReads: [(Date, Date)] { lock.withLock { _sampleReads } }
        let samples: [DriveSample]
        init(samples: [DriveSample]) { self.samples = samples }
        func driveRoute(id: String) async throws -> DriveRoute {
            lock.withLock { _routeReads.append(id) }
            return try Fixture.decode(DriveRoute.self, "drive_\(id)")
        }
        func driveSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [DriveSample] {
            lock.withLock { _sampleReads.append((since, until)) }
            return samples.filter { $0.at >= since && $0.at <= until }
        }
    }

    private func samples() throws -> [DriveSample] { try Fixture.decode([DriveSample].self, "samples_round_trip") }

    func testTheRowDecodesItsLegs() throws {
        let d = try row()
        XCTAssertEqual(d.roundTripLegs, 3, "round trip, 3 drives: the stub does not count")
        XCTAssertEqual(d.legs?.map(\.id), ["rt-leg-1", "rt-leg-2", "rt-leg-3"])
        XCTAssertEqual(d.legs?.map(\.parkedMinAfter), [13, 19, nil])
    }

    func testEveryLegIsReadAndTheTimelineSpansThemAll() async throws {
        let d = try row()
        let src = Fake(samples: try samples())
        let model = DriveDetailModel()
        await model.load(source: src, drive: d)
        XCTAssertEqual(Set(src.routeReads), ["rt-leg-1", "rt-leg-2", "rt-leg-3"], "one route read per leg")
        XCTAssertEqual(src.sampleReads.count, 3, "one samples read per leg's window")
        XCTAssertEqual(model.legs.count, 3)
        guard case .loaded(let first, let s) = model.phase else { return XCTFail("\(model.phase)") }
        XCTAssertEqual(first.endedAt, d.legs?.first?.endedAt)
        let all = try XCTUnwrap(s)
        XCTAssertEqual(Set(all.map(\.at)).count, all.count, "the legs' overlapping margins are not counted twice")
        XCTAssertEqual(all.map(\.at), all.map(\.at).sorted())
        XCTAssertTrue(all.contains { $0.at > d.legs![1].endedAt }, "samples of the later legs are there")
        XCTAssertNotNil(model.timeline)
    }

    func testAllThreeLegsAreDrawnWithAStopBetweenThem() throws {
        let d = try row()
        let all = try samples()
        let drawings = try d.legs!.map { leg -> RouteSnap.Drawing in
            let route = try Fixture.decode(DriveRoute.self, "drive_\(leg.id)")
            let path = try XCTUnwrap(RouteSnap.path(drive: route, samples: all))
            return RouteSnap.raw(path)
        }
        let merged = try XCTUnwrap(RouteSnap.merge(drawings))
        XCTAssertEqual(merged.start, drawings.first?.start)
        XCTAssertEqual(merged.end, drawings.last?.end)
        XCTAssertEqual(merged.runs.count, drawings.flatMap(\.runs).count)
        let single = try XCTUnwrap(drawings.first)
        XCTAssertGreaterThan(merged.allCoords.count, single.allCoords.count * 2, "more than the first leg")
        XCTAssertEqual(RouteSnap.parkedText(minutes: 13), "13 min")
        XCTAssertEqual(RouteSnap.parkedText(minutes: 65), "1 h 5 min")
        XCTAssertEqual(RouteSnap.parkedText(minutes: 120), "2 h")
    }

    func testAnOldServerSendsNoLegs() async throws {
        let d = try XCTUnwrap(Fixture.decode(ActivityResponse.self, "activity").drives.first { $0.id == "drv-2a" })
        XCTAssertNil(d.legs, "an old server sends no legs: the first leg's drawing stays")
    }
}
