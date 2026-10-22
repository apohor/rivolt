import XCTest
@testable import Rivolt

final class DriveRouteTests: XCTestCase {
    /// Google's own example for the encoded-polyline format.
    func testThePolylineDecodesAsGooglesExample() {
        let p = DriveRoute.decode("_p~iF~ps|U_ulLnnqC_mqNvxq`@")
        XCTAssertEqual(p.count, 3)
        XCTAssertEqual(p[0].lat, 38.5, accuracy: 1e-9)
        XCTAssertEqual(p[0].lon, -120.2, accuracy: 1e-9)
        XCTAssertEqual(p[2].lat, 43.252, accuracy: 1e-9)
        XCTAssertEqual(p[2].lon, -126.453, accuracy: 1e-9)
        // A truncated string keeps what it decoded.
        XCTAssertEqual(DriveRoute.decode("_p~iF~ps|U_ulL").count, 1)
        XCTAssertEqual(DriveRoute.decode(""), [])
    }

    func testTheRouteFallsBackToTheEndsAndDropsMissingFixes() throws {
        func route(_ polyline: String?, _ s: (Double, Double), _ e: (Double, Double)) throws -> DriveRoute {
            var json = #"{"ID":"d1","StartLat":\#(s.0),"StartLon":\#(s.1),"EndLat":\#(e.0),"EndLon":\#(e.1),"DistanceMi":12"#
            if let polyline { json += #","RoutePolyline":"\#(polyline)""# }
            json += "}"
            return try JSON.decoder().decode(DriveRoute.self, from: Data(json.utf8))
        }
        XCTAssertEqual(try route("_p~iF~ps|U_ulLnnqC_mqNvxq`@", (1, 2), (3, 4)).points.count, 3)
        // No trace: a straight line between the ends, as the web.
        XCTAssertEqual(try route(nil, (32.7767, -96.797), (29.7604, -95.3698)).points,
                       [.init(lat: 32.7767, lon: -96.797), .init(lat: 29.7604, lon: -95.3698)])
        // No fix at one end and no trace: nothing to draw.
        XCTAssertEqual(try route("", (0, 0), (29.7604, -95.3698)).points, [])
    }
}

final class MapStyleTests: XCTestCase {
    private let server = URL(string: "https://rivolt.example")!

    /// Every URL the bundled styles name is the app bundle's or the signed-in server's: no third-party host.
    func testTheBundledStylesNameOnlyTheBundleAndTheServer() throws {
        for dark in [false, true] {
            let json = try XCTUnwrap(MapStyle.json(dark: dark, server: server), "the bundled style is missing")
            let urls = MapStyle.urls(in: json)
            XCTAssertTrue(urls.contains("pmtiles://https://rivolt.example/api/maps/tiles/us.pmtiles"), "\(urls)")
            XCTAssertTrue(urls.contains("asset://Map/glyphs/{fontstack}/{range}.pbf"), "\(urls)")
            for u in urls { XCTAssertTrue(MapStyle.isAllowed(u, server: server), u) }
            XCTAssertFalse(json.contains(MapStyle.serverPlaceholder))
            // The tiles are OpenStreetMap data: the style's source carries the credit.
            XCTAssertTrue(json.contains("© OpenStreetMap"))
            XCTAssertNotNil(try JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any])
        }
    }

    func testOnlyTheServersOriginOrTheBundleIsAllowed() {
        XCTAssertTrue(MapStyle.isAllowed("https://rivolt.example/api/maps/tiles/us.pmtiles", server: server))
        XCTAssertFalse(MapStyle.isAllowed("https://api.mapbox.com/styles/v1/x", server: server))
        XCTAssertFalse(MapStyle.isAllowed("mapbox://styles/x", server: server))
        XCTAssertFalse(MapStyle.isAllowed("https://demotiles.maplibre.org/style.json", server: server))
        XCTAssertFalse(MapStyle.isAllowed("pmtiles://https://tiles.example/us.pmtiles", server: server))
        XCTAssertFalse(MapStyle.isAllowed("http://rivolt.example/api/maps/tiles/us.pmtiles", server: server))
        XCTAssertFalse(MapStyle.isAllowed("https://u:p@rivolt.example/api/maps/tiles/us.pmtiles", server: server))
        XCTAssertEqual(MapStyle.origin(URL(string: "https://rivolt.example:8443/some/path?q=1")!), "https://rivolt.example:8443")
    }

    /// The token rides only on the server's own origin.
    func testTheTokenGoesOnlyToTheServer() {
        let h = MapAuthHeader()
        h.set(origin: "https://rivolt.example", token: "t0k")
        XCTAssertEqual(h.header(for: URL(string: "https://rivolt.example/api/maps/tiles/us.pmtiles")), "Bearer t0k")
        XCTAssertNil(h.header(for: URL(string: "https://evil.example/api/maps/tiles/us.pmtiles")))
        XCTAssertNil(h.header(for: URL(string: "http://rivolt.example/api/maps/tiles/us.pmtiles")))
        let r = NSMutableURLRequest(url: URL(string: "https://evil.example/x")!)
        r.setValue("Bearer leaked", forHTTPHeaderField: "Authorization")
        XCTAssertNil(h.willSend(r).value(forHTTPHeaderField: "Authorization"))
        h.set(origin: nil, token: nil)
        XCTAssertNil(h.header(for: URL(string: "https://rivolt.example/api/maps/tiles/us.pmtiles")))
    }
}

final class ChargePlaceTests: XCTestCase {
    func testPlacesAreTheServersClustersWithoutTheirMembers() throws {
        let places = try Fixture.decode([ChargePlace].self, "charge_clusters")
        XCTAssertEqual(places.count, 4)
        // Only the cluster's label, centre and totals are held; the member charge ids are not decoded.
        XCTAssertEqual(Mirror(reflecting: places[0]).children.compactMap(\.label), ["label", "lat", "lon", "sessions", "energyKwh", "radiusM"])
        let shown = ChargePlace.shown(places)
        // The no-fix cluster is left out; the busiest place comes first.
        XCTAssertEqual(shown.map(\.title), ["Home", "Away", "Fast"])
        XCTAssertEqual(shown.map(\.key), [.home, .away, .dcfc])
        XCTAssertEqual(ChargePlace(label: "", lat: 1, lon: 1, sessions: 1, energyKwh: 1, radiusM: 1).title, "Other")
    }

    func testCirclesGrowWithSessionsWithinBounds() {
        func r(_ n: Int) -> Double { ChargePlace(label: "Home", lat: 1, lon: 1, sessions: n, energyKwh: 1, radiusM: 1).circleRadius }
        XCTAssertEqual(r(1), 6)
        XCTAssertEqual(r(5), 10, accuracy: 1e-9)
        XCTAssertEqual(r(500), 18)
        XCTAssertLessThan(r(2), r(10))
    }
}
