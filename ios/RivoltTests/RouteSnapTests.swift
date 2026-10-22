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
private func run(_ n: Int, mph: Double = 30, t0: Double = 1_800_000_000) -> [P] {
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
        var pts = run(6)
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
        let a = run(5)
        let late = run(5, t0: a.last!.t! + 300).map { P(lat: $0.lat + 0.01, lon: $0.lon, t: $0.t, s: $0.s) }
        let (segs, gaps) = RouteSnap.split(a + late)
        XCTAssertEqual(segs.count, 2)
        XCTAssertEqual(gaps.count, 1)
        XCTAssertEqual(gaps[0].from, C(lat: a.last!.lat, lon: a.last!.lon))
        XCTAssertEqual(RouteSnap.classify(run(4)), .trivial)
        XCTAssertEqual(RouteSnap.classify(run(5)), .short)
        XCTAssertEqual(RouteSnap.classify(run(10)), .normal)
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

    func testSamplesDecodeOnlyTheSixFields() throws {
        let json = #"[{"VehicleID":"veh-fixture","At":"2026-10-20T15:01:00Z","Lat":32.775,"Lon":-96.797,"SpeedMph":20.5,"ShiftState":"D","LocationFixAt":"2026-10-20T15:00:58Z","BatteryLevelPct":80,"OdometerMi":12000,"InsideTempC":21,"temp_f":70}]"#
        let s = try JSON.decoder().decode([DriveSample].self, from: Data(json.utf8))
        XCTAssertEqual(s[0].speedMph, 20.5)
        XCTAssertEqual(Mirror(reflecting: s[0]).children.compactMap(\.label), ["at", "lat", "lon", "speedMph", "shiftState", "locationFixAt"])
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
        return ValhallaTraceResponse(matchings: [.init(geometry: encode(snapped))], tracepoints: body.shape.map { _ in .init(matchingsIndex: 0) })
    }

    func valhallaRoute(_ body: ValhallaRouteRequest) async throws -> ValhallaRouteResponse {
        guard routeOK else { throw APIError.server(status: 400, message: "no route") }
        let pts = body.locations.map { C(lat: $0.lat, lon: $0.lon) }
        return ValhallaRouteResponse(trip: .init(legs: [.init(shape: encode(pts))]))
    }
}

final class RouteSnapSnappingTests: XCTestCase {
    private func path(_ pts: [P]) -> RouteSnap.Path {
        RouteSnap.Path(points: pts, start: C(lat: pts[0].lat, lon: pts[0].lon), end: C(lat: pts.last!.lat, lon: pts.last!.lon), sameSpot: false)
    }

    func testASnappedRouteCarriesTheSpeedsOfTheNearestSamples() async throws {
        let v = FakeValhalla()
        let d = try XCTUnwrap(await RouteSnap.snap(path(run(20, mph: 55)), source: v))
        XCTAssertEqual(v.traces, 1)
        XCTAssertFalse(d.pieces.isEmpty)
        XCTAssertEqual(Set(d.pieces.compactMap(\.speed)), [.to65])
        XCTAssertTrue(d.hasSpeed)
        // Snapped, not the recorded points.
        XCTAssertEqual(d.pieces[0].coords[0].lon, -96.797 + 0.0001, accuracy: 1e-6)
    }

    func testAFailedMatchKeepsTheRecordedLine() async {
        let snapped = await RouteSnap.snap(path(run(20)), source: FakeValhalla(failTrace: true))
        XCTAssertNil(snapped)
    }

    func testAShortStretchIsNeverSentToTheMatcher() async {
        let v = FakeValhalla()
        XCTAssertNil(await RouteSnap.snap(path(run(4)), source: v))
        XCTAssertEqual(v.traces, 0)
    }

    func testAGapIsRoutedWhenItCanBeAndDashedWhenNot() async throws {
        let a = run(10)
        let b = run(10, t0: a.last!.t! + 600).map { P(lat: $0.lat + 0.02, lon: $0.lon, t: $0.t, s: $0.s) }
        let dashed = try XCTUnwrap(await RouteSnap.snap(path(a + b), source: FakeValhalla()))
        XCTAssertEqual(dashed.gaps.count, 1)
        XCTAssertTrue(dashed.routedGaps.isEmpty)
        let routed = try XCTUnwrap(await RouteSnap.snap(path(a + b), source: FakeValhalla(routeOK: true)))
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
