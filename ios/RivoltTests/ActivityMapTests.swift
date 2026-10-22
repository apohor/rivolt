import MapLibre
import XCTest
@testable import Rivolt

/// The Activity map: the two lists narrowed to DA's fields, the month on the phone, the power buckets, what a tap
/// opens, and the map itself (sources, layers, the filter and the camera).
@MainActor
final class ActivityMapTests: XCTestCase {
    private let utc: Calendar = {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }()

    private func drive(_ id: String, _ at: String, polyline: String? = nil) -> String {
        let route = polyline.map { #""RoutePolyline":"\#($0)","# } ?? ""
        return #"{"ID":"\#(id)","VehicleID":"veh-fixture","StartedAt":"\#(at)","EndedAt":"\#(at)",\#(route)"StartLat":32.7767,"StartLon":-96.797,"EndLat":32.79,"EndLon":-96.81,"DistanceMi":3.2,"EstimatedCost":0.4,"Place":"away","StartAddress":"x"}"#
    }

    private func charge(_ id: String, _ at: String, kw: Double = 7, kind: String? = "ac", lat: Double = 32.7767) -> String {
        let k = kind.map { #""kind":"\#($0)","# } ?? ""
        return #"{"ID":"\#(id)","VehicleID":"veh-fixture","StartedAt":"\#(at)","EndedAt":"\#(at)",\#(k)"Lat":\#(lat),"Lon":-96.797,"MaxPowerKW":\#(kw),"Cost":12,"place":"home","Network":"EVgo","km_from_home":0.1}"#
    }

    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try JSON.decoder().decode(type, from: Data(json.utf8))
    }

    func testTheListsDecodeOnlyDAsFields() throws {
        let d = try decode(MapDrive.self, drive("d1", "2026-10-03T10:00:00Z", polyline: "_p~iF~ps|U_ulLnnqC"))
        XCTAssertEqual(Mirror(reflecting: d).children.compactMap(\.label), ["id", "startedAt", "startLat", "startLon", "endLat", "endLon", "routePolyline"])
        XCTAssertEqual(d.points.count, 2)
        let c = try decode(MapCharge.self, charge("c1", "2026-10-03T12:00:00Z"))
        XCTAssertEqual(Mirror(reflecting: c).children.compactMap(\.label), ["id", "startedAt", "lat", "lon", "maxPowerKW", "kind"])
        // No stored trace: the straight line between the ends.
        let straight = try decode(MapDrive.self, drive("d2", "2026-10-03T10:00:00Z"))
        XCTAssertEqual(straight.points, [.init(lat: 32.7767, lon: -96.797), .init(lat: 32.79, lon: -96.81)])
    }

    func testTheWebsPowerBuckets() throws {
        XCTAssertEqual(try decode(MapCharge.self, charge("a", "2026-10-03T12:00:00Z", kw: 7, kind: "dcfc")).bucket, .fast)
        XCTAssertEqual(try decode(MapCharge.self, charge("b", "2026-10-03T12:00:00Z", kw: 150, kind: nil)).bucket, .fast)
        XCTAssertEqual(try decode(MapCharge.self, charge("c", "2026-10-03T12:00:00Z", kw: 150, kind: "ac")).bucket, .l2)
        XCTAssertEqual(try decode(MapCharge.self, charge("d", "2026-10-03T12:00:00Z", kw: 7)).bucket, .l2)
        XCTAssertEqual(try decode(MapCharge.self, charge("e", "2026-10-03T12:00:00Z", kw: 1.4)).bucket, .l1)
        XCTAssertEqual(MapCharge.Bucket.allCases.map(\.label), ["Fast", "L2", "L1"])
    }

    private final class Fake: ActivityMapSource, @unchecked Sendable {
        var drives: [MapDrive] = []
        var charges: [MapCharge] = []
        var fail = false
        private(set) var reads = 0
        func mapDrives(vehicleID: String) async throws -> [MapDrive] {
            reads += 1
            if fail { throw APIError.server(status: 500, message: "no") }
            return drives
        }
        func mapCharges(vehicleID: String) async throws -> [MapCharge] {
            if fail { throw APIError.server(status: 500, message: "no") }
            return charges
        }
    }

    func testTheModelKeepsTheMonthAndForgetsItOnAnotherMonthOrSignOut() async throws {
        let src = Fake()
        src.drives = [try decode(MapDrive.self, drive("oct", "2026-10-03T10:00:00Z")), try decode(MapDrive.self, drive("sep", "2026-09-30T23:00:00Z"))]
        src.charges = [try decode(MapCharge.self, charge("c-oct", "2026-10-04T10:00:00Z")),
                       try decode(MapCharge.self, charge("c-nofix", "2026-10-05T10:00:00Z", lat: 0)).withLon(0)]
        let model = ActivityMapModel()
        let oct = PeriodMonth(containing: utc.date(from: DateComponents(year: 2026, month: 10, day: 15))!, calendar: utc)
        await model.load(source: src, vehicleID: "veh-fixture", month: oct, calendar: utc)
        XCTAssertEqual(model.drives.map(\.id), ["oct"])
        XCTAssertEqual(model.charges.map(\.id), ["c-oct"])
        // The same month again is not read again.
        await model.load(source: src, vehicleID: "veh-fixture", month: oct, calendar: utc)
        XCTAssertEqual(src.reads, 1)
        // Another month: only its rows.
        await model.load(source: src, vehicleID: "veh-fixture", month: oct.shifted(by: -1, calendar: utc), calendar: utc)
        XCTAssertEqual(model.drives.map(\.id), ["sep"])
        XCTAssertTrue(model.charges.isEmpty)
        // Sign-out or another server.
        model.clear()
        XCTAssertTrue(model.drives.isEmpty)
        XCTAssertNil(model.key)
        // A failed read for another truck shows nothing of the last one.
        await model.load(source: src, vehicleID: "veh-fixture", month: oct, calendar: utc)
        src.fail = true
        await model.load(source: src, vehicleID: "veh-other", month: oct, calendar: utc)
        XCTAssertTrue(model.failed)
        XCTAssertTrue(model.drives.isEmpty)
    }

    /// Long routes are simplified (about 5 m), keeping their ends and their shape.
    func testRoutesAreSimplified() {
        let straight = (0..<500).map { DriveRoute.Point(lat: 32.7 + Double($0) * 1e-4, lon: -96.8) }
        XCTAssertEqual(ActivityMapModel.simplify(straight, tolerance: ActivityMapModel.tolerance), [straight[0], straight[499]])
        let corner = [DriveRoute.Point(lat: 0, lon: 0), .init(lat: 0.5, lon: 0.000001), .init(lat: 1, lon: 0), .init(lat: 1, lon: 1)]
        XCTAssertEqual(ActivityMapModel.simplify(corner, tolerance: ActivityMapModel.tolerance), [corner[0], corner[2], corner[3]])
        XCTAssertEqual(ActivityMapModel.simplify(Array(corner.prefix(2)), tolerance: 1), Array(corner.prefix(2)))
    }

    func testATapOpensTheDriveOrChargeOfTheMonth() throws {
        let r = try Fixture.decode(ActivityResponse.self, "activity")
        let d = r.drives[0], c = r.charges[0]
        XCTAssertEqual(ActivityLogic.item(for: .drive(id: d.id, startedAt: d.startedAt), in: r), .drive(d))
        // A round trip's later leg: its own id isn't in the list; the drive that spans it is.
        XCTAssertEqual(ActivityLogic.item(for: .drive(id: "leg-2", startedAt: d.startedAt.addingTimeInterval(60)), in: r), .drive(d))
        XCTAssertEqual(ActivityLogic.item(for: .charge(id: c.id), in: r), .charge(c))
        XCTAssertNil(ActivityLogic.item(for: .charge(id: "elsewhere"), in: r))
        XCTAssertEqual(ActivityMapRepresentable.Coordinator.pick(["id": "c1", "bucket": 0]), .charge(id: "c1"))
        XCTAssertEqual(ActivityMapRepresentable.Coordinator.pick(["id": "d1", "at": NSNumber(value: 1_800_000_000.0)]),
                       .drive(id: "d1", startedAt: Date(timeIntervalSince1970: 1_800_000_000)))
        XCTAssertNil(ActivityMapRepresentable.Coordinator.pick(["at": 1]))
    }

    func testTheMapDrawsTheMonthAndFitsWhatTheFilterShows() throws {
        let d1 = try decode(MapDrive.self, drive("d1", "2026-10-03T10:00:00Z"))
        let drives = [ActivityMapModel.Drive(id: d1.id, startedAt: d1.startedAt, points: d1.points)]
        let charges = [try decode(MapCharge.self, charge("c1", "2026-10-04T10:00:00Z", kw: 150, kind: "dcfc", lat: 30.2672))]
        let server = URL(string: "https://rivolt.example")!
        let coordinator = ActivityMapRepresentable.Coordinator(drives: drives, charges: charges, filter: .all, onHealth: { _ in })
        let map = MLNMapView.displayOnly(styleJSON: try XCTUnwrap(MapStyle.json(dark: false, server: server)), watch: coordinator, interactive: true)
        XCTAssertTrue(map.isScrollEnabled && map.isZoomEnabled)
        XCTAssertFalse(map.isRotateEnabled || map.isPitchEnabled)
        let deadline = Date().addingTimeInterval(5)
        while map.style == nil, Date() < deadline { RunLoop.main.run(until: Date().addingTimeInterval(0.05)) }
        let style = try XCTUnwrap(map.style)
        for name in ["drives", "ends", "charges"] {
            XCTAssertNotNil(style.source(withIdentifier: ActivityMapRepresentable.Coordinator.layerID(name)), name)
        }
        map.frame = CGRect(x: 0, y: 0, width: 360, height: 480)
        map.layoutIfNeeded()
        // Dallas and Austin both in view, not the world.
        XCTAssertGreaterThan(map.zoomLevel, 4)
        XCTAssertEqual(map.centerCoordinate.latitude, 31.5, accuracy: 0.5)
        // Drives only: the charge layers hide and the camera comes to the drive.
        coordinator.update(drives: drives, charges: charges, filter: .drives, on: map)
        XCTAssertEqual(style.layer(withIdentifier: ActivityMapRepresentable.Coordinator.layerID("charge-0"))?.isVisible, false)
        XCTAssertEqual(style.layer(withIdentifier: ActivityMapRepresentable.Coordinator.layerID("drives"))?.isVisible, true)
        XCTAssertEqual(map.centerCoordinate.latitude, 32.78, accuracy: 0.05)
        XCTAssertLessThanOrEqual(map.zoomLevel, ActivityMapRepresentable.maxFitZoom + 0.01)
    }
}

private extension MapCharge {
    /// A copy at another longitude (a fix at 0,0 for the no-fix case).
    func withLon(_ lon: Double) -> MapCharge {
        let json = #"{"ID":"\#(id)","StartedAt":"\#(ISO8601DateFormatter().string(from: startedAt))","Lat":\#(lat),"Lon":\#(lon),"MaxPowerKW":\#(maxPowerKW ?? 0)}"#
        return try! JSON.decoder().decode(MapCharge.self, from: Data(json.utf8))
    }
}
