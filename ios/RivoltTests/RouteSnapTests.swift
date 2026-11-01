import XCTest
@testable import Rivolt

private typealias P = RouteSnap.Point
private typealias C = RouteSnap.Coord

/// Google's encoded-polyline format, to build Valhalla answers in tests.
private func encode(_ pts: [C], precision: Int = 6) -> String {
    let f = precision == 6 ? 1e6 : 1e5
    var out = ""
    var plat = 0, plon = 0
    func put(_ v: Int) {
        var v = v < 0 ? ~(v << 1) : v << 1
        while v >= 0x20 {
            out.append(Character(UnicodeScalar(UInt8((0x20 | (v & 0x1f)) + 63))))
            v >>= 5
        }
        out.append(Character(UnicodeScalar(UInt8(v + 63))))
    }
    for p in pts {
        let lat = Int((p.lat * f).rounded()), lon = Int((p.lon * f).rounded())
        put(lat - plat)
        put(lon - plon)
        plat = lat
        plon = lon
    }
    return out
}

/// A straight run north from downtown Dallas: `n` points ~111 m apart, 10 s apart, at `mph`.
private func straight(_ n: Int, mph: Double = 30, t0: Double = 1_800_000_000) -> [P] {
    (0..<n).map { P(lat: 32.7767 + Double($0) * 0.001, lon: -96.797, t: t0 + Double($0) * 10, s: mph) }
}

final class RouteSnapLogicTests: XCTestCase {
    func testSpeedBucketsAreTheWebs() {
        XCTAssertEqual(RouteSnap.Speed.bucket(4.9), .under5)
        XCTAssertEqual(RouteSnap.Speed.bucket(5), .to25)
        XCTAssertEqual(RouteSnap.Speed.bucket(25), .to50)
        XCTAssertEqual(RouteSnap.Speed.bucket(64.9), .to65)
        XCTAssertEqual(RouteSnap.Speed.bucket(65), .over65)
        XCTAssertNil(RouteSnap.Speed.bucket(nil))
        XCTAssertNil(RouteSnap.Speed.bucket(.nan))
        XCTAssertEqual(RouteSnap.Speed.allCases.map(\.label), ["<5", "5–25", "25–50", "50–65", "65+"])
    }

    func testCleaningDropsDuplicatesJumpsAndStopRuns() {
        var pts = straight(6)
        // A repeat, then a single fix 50 km off (impossible both ways).
        pts.insert(pts[1], at: 2)
        pts.insert(P(lat: 33.2, lon: -96.797, t: pts[3].t! + 1, s: 30), at: 3)
        let cleaned = RouteSnap.clean(pts)
        XCTAssertEqual(cleaned.count, 6)
        XCTAssertFalse(cleaned.contains { $0.lat == 33.2 })
        // Five readings in the same spot within a minute collapse to the run's two ends.
        let still = (0..<5).map { P(lat: 32.7767, lon: -96.797, t: 1_800_000_000 + Double($0) * 5, s: 0) }
        let moving = [P(lat: 32.79, lon: -96.797, t: 1_800_000_100, s: 30)]
        let jitter = still.enumerated().map { i, p in P(lat: p.lat + Double(i) * 1e-7, lon: p.lon, t: p.t, s: 0) }
        XCTAssertEqual(RouteSnap.clean(jitter + moving).count, 3)
    }

    func testSplittingOnTimeAndDistanceGaps() {
        let a = straight(5)
        let late = straight(5, t0: a.last!.t! + 300).map { P(lat: $0.lat + 0.01, lon: $0.lon, t: $0.t, s: $0.s) }
        let (segs, gaps) = RouteSnap.split(a + late)
        XCTAssertEqual(segs.count, 2)
        XCTAssertEqual(gaps.count, 1)
        XCTAssertEqual(gaps[0].from, C(lat: a.last!.lat, lon: a.last!.lon))
        XCTAssertEqual(RouteSnap.classify(straight(4)), .trivial)
        XCTAssertEqual(RouteSnap.classify(straight(5)), .short)
        XCTAssertEqual(RouteSnap.classify(straight(10)), .normal)
    }

    func testPiecesChangeAtEachBucketAndShareTheirEnds() {
        // Each stretch takes its first point's bucket; the last point only ends the line.
        let coords = (0..<6).map { C(lat: Double($0), lon: 0) }
        let pieces = RouteSnap.pieces(coords, speeds: [3, 3, 30, 30, 70, 70])
        XCTAssertEqual(pieces.map(\.speed), [.under5, .to50, .over65])
        XCTAssertEqual(pieces[0].coords.last, pieces[1].coords.first)
        XCTAssertEqual(pieces[1].coords.last, pieces[2].coords.first)
        XCTAssertEqual(RouteSnap.pieces(coords, speeds: []).map(\.speed), [nil])
    }

    private func drive(_ fields: String = "") throws -> DriveRoute {
        let json = #"{"ID":"d1","VehicleID":"veh-fixture","StartedAt":"2026-10-20T15:00:00Z","EndedAt":"2026-10-20T15:10:00Z","StartLat":32.78,"StartLon":-96.80,"EndLat":32.80,"EndLon":-96.80\#(fields)}"#
        return try JSON.decoder().decode(DriveRoute.self, from: Data(json.utf8))
    }

    private func sample(_ at: String, _ lat: Double, _ lon: Double, _ mph: Double?, _ gear: String) -> DriveSample {
        DriveSample(at: JSON.parseDate(at)!, lat: lat, lon: lon, speedMph: mph, shiftState: gear, locationFixAt: nil)
    }

    /// The web's pins: the last parked fix before the drive and the first after; the path trimmed of parked frames.
    func testThePathUsesTheParkedFixesAroundTheDrive() throws {
        let d = try drive()
        let samples = [
            sample("2026-10-20T14:52:00Z", 32.7700, -96.7970, 0, "P"),
            sample("2026-10-20T14:58:00Z", 32.7701, -96.7970, 0, "P"),
            sample("2026-10-20T15:00:05Z", 32.7702, -96.7970, 0, "P"),
            sample("2026-10-20T15:01:00Z", 32.7750, -96.7970, 20, "D"),
            sample("2026-10-20T15:05:00Z", 32.7850, -96.7970, 45, "D"),
            sample("2026-10-20T15:09:30Z", 32.7950, -96.7970, 70, "D"),
            sample("2026-10-20T15:09:50Z", 32.7950, -96.7970, 0, "P"),
            sample("2026-10-20T15:12:00Z", 32.7960, -96.7970, 0, "P"),
            sample("2026-10-20T15:25:00Z", 32.9000, -96.7970, 0, "P"),
        ]
        let path = try XCTUnwrap(RouteSnap.path(drive: d, samples: samples))
        XCTAssertEqual(path.start, C(lat: 32.7701, lon: -96.7970))
        XCTAssertEqual(path.end, C(lat: 32.7960, lon: -96.7970))
        XCTAssertFalse(path.sameSpot)
        // The pins bracket the three in-drive samples (the parked frames at either end are trimmed), parked at 0 mph.
        XCTAssertEqual(path.points.map(\.s), [0, 20, 45, 70, 0])
        XCTAssertEqual(path.points.first?.t, JSON.parseDate("2026-10-20T15:01:00Z")!.timeIntervalSince1970 - 60)
        let raw = RouteSnap.raw(path)
        XCTAssertTrue(raw.hasSpeed)
        XCTAssertEqual(raw.pieces.compactMap(\.speed), [.under5, .to25, .to50, .over65])
    }

    func testARoundTripHasOnePin() throws {
        let d = try drive()
        let samples = [
            sample("2026-10-20T14:58:00Z", 32.7701, -96.7970, 0, "P"),
            sample("2026-10-20T15:03:00Z", 32.7800, -96.7970, 30, "D"),
            sample("2026-10-20T15:07:00Z", 32.7750, -96.7970, 30, "D"),
            sample("2026-10-20T15:11:00Z", 32.77012, -96.79701, 0, "P"),
        ]
        let path = try XCTUnwrap(RouteSnap.path(drive: d, samples: samples))
        XCTAssertTrue(path.sameSpot)
        XCTAssertNil(path.end)
        XCTAssertEqual(path.points.count, 3)
        // No in-drive fix: nothing to snap, the caller keeps the stored route.
        XCTAssertNil(RouteSnap.path(drive: d, samples: [sample("2026-10-20T14:58:00Z", 32.77, -96.79, 0, "P")]))
    }

    func testSamplesDecodeOnlyDAsNineFields() throws {
        let json = #"[{"VehicleID":"veh-fixture","At":"2026-10-20T15:01:00Z","Lat":32.775,"Lon":-96.797,"SpeedMph":20.5,"ShiftState":"D","LocationFixAt":"2026-10-20T15:00:58Z","BatteryLevelPct":80,"altitude_m":152.4,"drive_mode":"everyday","OdometerMi":12000,"InsideTempC":21,"temp_f":70,"RangeMi":220}]"#
        let s = try JSON.decoder().decode([DriveSample].self, from: Data(json.utf8))
        XCTAssertEqual(s[0].speedMph, 20.5)
        XCTAssertEqual(s[0].batteryLevelPct, 80)
        XCTAssertEqual(s[0].altitudeM, 152.4)
        XCTAssertEqual(s[0].driveMode, "everyday")
        XCTAssertEqual(Mirror(reflecting: s[0]).children.compactMap(\.label),
                       ["at", "lat", "lon", "speedMph", "shiftState", "locationFixAt", "batteryLevelPct", "altitudeM", "driveMode"])
    }

    func testTheValhallaBodiesAreTheWebs() throws {
        let enc = JSONEncoder()
        enc.keyEncodingStrategy = .convertToSnakeCase
        let trace = try XCTUnwrap(JSONSerialization.jsonObject(with: enc.encode(ValhallaTraceRequest(shape: [.init(lat: 1, lon: 2, time: 3), .init(lat: 4, lon: 5, time: nil)]))) as? [String: Any])
        XCTAssertEqual(trace["costing"] as? String, "auto")
        XCTAssertEqual(trace["shape_match"] as? String, "walk_or_snap")
        XCTAssertEqual(trace["format"] as? String, "osrm")
        XCTAssertEqual((trace["trace_options"] as? [String: Int]), ["search_radius": 100, "gps_accuracy": 25])
        let shape = try XCTUnwrap(trace["shape"] as? [[String: Double]])
        XCTAssertEqual(shape[0], ["lat": 1, "lon": 2, "time": 3])
        XCTAssertEqual(shape[1], ["lat": 4, "lon": 5])
        let route = try XCTUnwrap(JSONSerialization.jsonObject(with: enc.encode(ValhallaRouteRequest(locations: [.init(lat: 1, lon: 2)]))) as? [String: Any])
        XCTAssertEqual((route["locations"] as? [[String: Any]])?.first?["type"] as? String, "break")
        XCTAssertEqual((route["directions_options"] as? [String: String]), ["units": "miles"])
    }
}

/// A fake Valhalla: the trace snaps to the same points nudged 10 m east; routes fail unless told otherwise.
private final class FakeValhalla: RouteSnapSource, @unchecked Sendable {
    private let lock = NSLock()
    private var _traces = 0
    var traces: Int { lock.withLock { _traces } }
    let failTrace: Bool
    let routeOK: Bool

    init(failTrace: Bool = false, routeOK: Bool = false) {
        self.failTrace = failTrace
        self.routeOK = routeOK
    }

    func valhallaTrace(_ body: ValhallaTraceRequest) async throws -> ValhallaTraceResponse {
        lock.withLock { _traces += 1 }
        if failTrace { throw APIError.server(status: 500, message: "no") }
        let snapped = body.shape.map { C(lat: $0.lat, lon: $0.lon + 0.0001) }
        let tracepoints: [ValhallaTraceResponse.Tracepoint?] = body.shape.map { _ in ValhallaTraceResponse.Tracepoint(matchingsIndex: 0) }
        return ValhallaTraceResponse(matchings: [ValhallaTraceResponse.Matching(geometry: encode(snapped))], tracepoints: tracepoints)
    }

    func valhallaRoute(_ body: ValhallaRouteRequest) async throws -> ValhallaRouteResponse {
        guard routeOK else { throw APIError.server(status: 400, message: "no route") }
        let pts = body.locations.map { C(lat: $0.lat, lon: $0.lon) }
        return ValhallaRouteResponse(trip: ValhallaRouteResponse.Trip(legs: [ValhallaRouteResponse.Trip.Leg(shape: encode(pts))]))
    }
}

final class RouteSnapSnappingTests: XCTestCase {
    private func path(_ pts: [P]) -> RouteSnap.Path {
        RouteSnap.Path(points: pts, start: C(lat: pts[0].lat, lon: pts[0].lon), end: C(lat: pts.last!.lat, lon: pts.last!.lon), sameSpot: false)
    }

    func testASnappedRouteCarriesTheSpeedsOfTheNearestSamples() async throws {
        let v = FakeValhalla()
        let snapped = await RouteSnap.snap(path(straight(20, mph: 55)), source: v)
        let d = try XCTUnwrap(snapped)
        XCTAssertEqual(v.traces, 1)
        XCTAssertFalse(d.pieces.isEmpty)
        XCTAssertEqual(Set(d.pieces.compactMap { $0.speed }), [RouteSnap.Speed.to65])
        XCTAssertTrue(d.hasSpeed)
        // Snapped, not the recorded points.
        XCTAssertEqual(d.pieces[0].coords[0].lon, -96.797 + 0.0001, accuracy: 1e-6)
    }

    func testAFailedMatchKeepsTheRecordedLine() async {
        let snapped = await RouteSnap.snap(path(straight(20)), source: FakeValhalla(failTrace: true))
        XCTAssertNil(snapped)
    }

    func testAShortStretchIsNeverSentToTheMatcher() async {
        let v = FakeValhalla()
        let snapped = await RouteSnap.snap(path(straight(4)), source: v)
        XCTAssertNil(snapped)
        XCTAssertEqual(v.traces, 0)
    }

    func testAGapIsRoutedWhenItCanBeAndDashedWhenNot() async throws {
        let a = straight(10)
        let b = straight(10, t0: a.last!.t! + 600).map { P(lat: $0.lat + 0.02, lon: $0.lon, t: $0.t, s: $0.s) }
        let unrouted = await RouteSnap.snap(path(a + b), source: FakeValhalla())
        let dashed = try XCTUnwrap(unrouted)
        XCTAssertEqual(dashed.gaps.count, 1)
        XCTAssertTrue(dashed.routedGaps.isEmpty)
        let filled = await RouteSnap.snap(path(a + b), source: FakeValhalla(routeOK: true))
        let routed = try XCTUnwrap(filled)
        XCTAssertTrue(routed.gaps.isEmpty)
        XCTAssertEqual(routed.routedGaps.count, 1)
    }
}

/// Answers the client in place of the server, by method and path.
private final class SnapStub: URLProtocol {
    nonisolated(unsafe) static var calls: [String] = []
    nonisolated(unsafe) static var respond: (String) -> (Int, Data) = { _ in (500, Data()) }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let path = request.url.map { $0.path + ($0.query.map { "?" + $0 } ?? "") } ?? ""
        let call = (request.httpMethod ?? "GET") + " " + path
        Self.calls.append(call)
        let (status, data) = Self.respond(call)
        let resp = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private struct SnapNoTokens: TokenStore {
    func token(for server: URL) throws -> String? { nil }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

final class RouteSnapClientTests: XCTestCase {
    private func client() -> APIClient {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [SnapStub.self]
        return APIClient(baseURL: URL(string: "https://rivolt.example")!, tokens: SnapNoTokens(), session: URLSession(configuration: config))
    }

    override func setUp() {
        super.setUp()
        SnapStub.calls = []
    }

    func testSamplesAskForTheDrivesTruckAndWindow() async throws {
        SnapStub.respond = { _ in (200, Data("[]".utf8)) }
        let since = try XCTUnwrap(JSON.parseDate("2026-10-20T14:50:00Z")), until = try XCTUnwrap(JSON.parseDate("2026-10-20T15:20:00Z"))
        let rows = try await client().driveSamples(since: since, until: until, vehicleID: "veh-fixture")
        XCTAssertTrue(rows.isEmpty)
        XCTAssertEqual(SnapStub.calls, ["GET /api/samples?since=2026-10-20T14:50:00Z&until=2026-10-20T15:20:00Z&limit=2000&vehicle=veh-fixture"])
    }

    func testValhallaGoesThroughTheServersTwoPosts() async throws {
        SnapStub.respond = { call in
            call.hasSuffix("/trace_route") ? (200, Data(#"{"matchings":[],"tracepoints":[]}"#.utf8)) : (200, Data(#"{"trip":{"legs":[]}}"#.utf8))
        }
        let c = client()
        _ = try await c.valhallaTrace(ValhallaTraceRequest(shape: [.init(lat: 1, lon: 2, time: nil)]))
        _ = try await c.valhallaRoute(ValhallaRouteRequest(locations: [.init(lat: 1, lon: 2), .init(lat: 3, lon: 4)]))
        XCTAssertEqual(SnapStub.calls, ["POST /api/maps/valhalla/trace_route", "POST /api/maps/valhalla/route"])
    }
}

/// The drive map's line in GPS-fix order, not arrival order (DriveTrack; the web's cleanTrack). Plainly fake places near (10, 10).
final class DriveTrackTests: XCTestCase {
    private let t0 = 1_800_000_000.0

    /// A driving sample that arrives `at` seconds in, at position step `step` along a line, with its GPS fix taken `fix` seconds in (nil: no fix time).
    private func s(_ at: Double, _ step: Double, fix: Double? = nil, lat: Double? = nil) -> DriveSample {
        DriveSample(at: Date(timeIntervalSince1970: t0 + at), lat: lat ?? 10 + step * 0.001, lon: 10 + step * 0.001, speedMph: 40, shiftState: "D",
                    locationFixAt: fix.map { Date(timeIntervalSince1970: t0 + $0) })
    }

    /// The step along the line each kept sample is at, to compare with what was sent.
    private func steps(_ xs: [DriveSample]) -> [Int] { xs.map { Int((($0.lat - 10) / 0.001).rounded()) } }

    func testACleanTrackIsUnchanged() {
        XCTAssertEqual(steps(DriveTrack.clean([s(0, 0, fix: 0), s(5, 1, fix: 5), s(10, 2, fix: 10)])), [0, 1, 2])
    }

    func testAFixOlderThanTheNewestKeptIsDropped() {
        // d arrives after c but carries b's older fix: drawn in arrival order the line would go 0, 1, 2, back to 1, then 3.
        let xs = [s(0, 0, fix: 0), s(5, 1, fix: 5), s(10, 2, fix: 10), s(15, 1, fix: 5), s(20, 3, fix: 20)]
        XCTAssertEqual(steps(DriveTrack.clean(xs)), [0, 1, 2, 3])
    }

    func testARepeatOfThePreviousFixIsDropped() {
        let xs = [s(0, 0, fix: 0), s(5, 1, fix: 5), s(10, 1, fix: 5), s(15, 1, fix: 5), s(20, 2, fix: 20)]
        XCTAssertEqual(steps(DriveTrack.clean(xs)), [0, 1, 2])
    }

    func testInterleavedLateFixesEndStrictlyForwardInFixTime() {
        // Every fix is stamped before the frame that carries it arrives.
        let xs = [s(0, 0, fix: 0), s(30, 4, fix: 20), s(31, 1, fix: 5), s(32, 2, fix: 10), s(40, 5, fix: 25), s(41, 3, fix: 15)]
        let out = DriveTrack.clean(xs)
        XCTAssertEqual(steps(out), [0, 4, 5])
        let fixes = out.compactMap(\.locationFixAt)
        XCTAssertEqual(fixes, fixes.sorted())
    }

    func testASampleWithNoFixTimeIsTakenAsFixedWhenItArrived() {
        XCTAssertEqual(steps(DriveTrack.clean([s(0, 0), s(5, 1), s(10, 2, fix: 10), s(15, 3)])), [0, 1, 2, 3])
    }

    func testAFixStampedAfterItsOwnFrameIsNotBelieved() {
        // b claims a fix an hour after it arrived. Believed, every later sample would look older and vanish.
        let xs = [s(0, 0, fix: 0), s(5, 1, fix: 3600), s(10, 2, fix: 10), s(15, 3, fix: 15)]
        XCTAssertEqual(steps(DriveTrack.clean(xs)), [0, 1, 2, 3])
    }

    func testASampleWithNoPositionPassesThroughAndMovesNothing() {
        let noPos = s(7, 0, fix: 7, lat: 0).with(lon: 0)
        XCTAssertEqual(DriveTrack.clean([s(0, 0, fix: 0), s(5, 1, fix: 5), noPos, s(10, 2, fix: 10)]).count, 4)
    }

    func testUnsortedInputGivesTheSameAnswer() {
        let xs = [s(10, 2, fix: 10), s(0, 0, fix: 0), s(15, 1, fix: 5), s(5, 1, fix: 5)]
        XCTAssertEqual(steps(DriveTrack.clean(xs)), [0, 1, 2])
    }

    func testNothingAndOneSample() {
        XCTAssertTrue(DriveTrack.clean([]).isEmpty)
        XCTAssertEqual(steps(DriveTrack.clean([s(0, 0, fix: 0)])), [0])
    }

    func testAFrozenFixLeavesTheFirstSample() {
        XCTAssertEqual(steps(DriveTrack.clean([s(0, 0, fix: 0), s(5, 0, fix: 0), s(10, 0, fix: 0)])), [0])
    }

    /// Through RouteSnap.path, as the drive screen builds the line: every third sample repeats the previous fix and every seventh carries the
    /// fix of the sample four steps back, so in arrival order the line would jump back. The path only ever moves forward.
    func testThePathOnlyMovesForward() throws {
        let json = #"{"ID":"d1","VehicleID":"veh-fixture","StartedAt":"2026-10-20T15:00:00Z","EndedAt":"2026-10-20T15:10:00Z","StartLat":10.0,"StartLon":10.0,"EndLat":10.07,"EndLon":10.07}"#
        let d = try JSON.decoder().decode(DriveRoute.self, from: Data(json.utf8))
        let start = JSON.parseDate("2026-10-20T15:01:00Z")!.timeIntervalSince1970 - t0
        let samples: [DriveSample] = (0..<60).map { i in
            var step = Double(i)
            if i % 7 == 0 && i > 4 { step = Double(i - 4) } else if i % 3 == 0 && i > 0 { step = Double(i - 1) }
            return s(start + Double(i) * 5, step + 1, fix: start + step * 5)
        }
        let path = try XCTUnwrap(RouteSnap.path(drive: d, samples: samples))
        XCTAssertLessThan(path.points.count, 60 + 2)
        XCTAssertGreaterThan(path.points.count, 30)
        for (a, b) in zip(path.points, path.points.dropFirst()) {
            XCTAssertLessThanOrEqual(a.lat, b.lat, "the line went back")
            XCTAssertLessThanOrEqual(a.lon, b.lon, "the line went back")
        }
    }
}

private extension DriveSample {
    /// The same sample at another longitude (for a sample with no position at all).
    func with(lon: Double) -> DriveSample {
        DriveSample(at: at, lat: lat, lon: lon, speedMph: speedMph, shiftState: shiftState, locationFixAt: locationFixAt)
    }
}
