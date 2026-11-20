import CoreLocation
import UIKit
import XCTest
@testable import Rivolt

/// The share card's basemap: the style carries no labels, the camera frames the route with room round it and never closer than
/// zoom 12, and the painter lays the picture under the line.
final class ShareBasemapTests: XCTestCase {
    private let server = URL(string: "https://rivolt.example")!

    private func path(_ pts: [(Double, Double)]) -> [DriveRoute.Point] { pts.map { DriveRoute.Point(lat: $0.0, lon: $0.1) } }

    /// A made-up winding line, nowhere real.
    private func winding(_ n: Int, span: Double) -> [DriveRoute.Point] {
        (0...n).map { i in
            let t = Double(i) / Double(n)
            return DriveRoute.Point(lat: 10 + t * span + span / 8 * sin(t * 9), lon: 20 + t * span * 1.7 + span / 6 * cos(t * 7))
        }
    }

    func testTheStyleHasNoLabelsGlyphsOrSprite() throws {
        let json = try XCTUnwrap(ShareBasemap.style(server: server), "the bundled style is missing")
        let root = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any])
        let layers = try XCTUnwrap(root["layers"] as? [[String: Any]])
        XCTAssertFalse(layers.isEmpty)
        XCTAssertTrue(layers.allSatisfy { ($0["type"] as? String) != "symbol" }, "a symbol layer draws a label")
        XCTAssertNil(root["glyphs"])
        XCTAssertNil(root["sprite"])
        let sources = try XCTUnwrap(root["sources"] as? [String: Any])
        XCTAssertEqual(Array(sources.keys), ["protomaps"])
        let urls = MapStyle.urls(in: json)
        XCTAssertEqual(urls.count, 1, "\(urls)")
        for u in urls { XCTAssertTrue(MapStyle.isAllowed(u, server: server), u) }
        XCTAssertFalse(json.contains(MapStyle.serverPlaceholder))
    }

    func testTheCameraKeepsTenPercentRoundTheRouteAndNeverGoesPastZoom12() throws {
        let size = ShareBasemap.size
        // A long drive: every point sits inside the padded box.
        let long = winding(120, span: 0.6)
        let f = try XCTUnwrap(ShareBasemap.frame([long]))
        XCTAssertLessThan(f.zoom, ShareBasemap.maxZoom)
        let world = 512 * pow(2, f.zoom)
        func px(_ p: DriveRoute.Point) -> CGPoint {
            func mx(_ lon: Double) -> Double { lon / 360 + 0.5 }
            func my(_ lat: Double) -> Double { 0.5 - log(tan(.pi / 4 + lat * .pi / 360)) / (2 * .pi) }
            return CGPoint(x: size.width / 2 + (mx(p.lon) - mx(f.center.longitude)) * world, y: size.height / 2 + (my(p.lat) - my(f.center.latitude)) * world)
        }
        for p in long {
            let q = px(p)
            XCTAssertGreaterThanOrEqual(q.x, size.width * 0.1 - 0.5)
            XCTAssertLessThanOrEqual(q.x, size.width * 0.9 + 0.5)
            XCTAssertGreaterThanOrEqual(q.y, size.height * 0.1 - 0.5)
            XCTAssertLessThanOrEqual(q.y, size.height * 0.9 + 0.5)
        }
        // A short drive (a few hundred metres): capped at 12, so the map stays a street grid.
        let short = try XCTUnwrap(ShareBasemap.frame([path([(10, 20), (10.002, 20.003)])]))
        XCTAssertEqual(short.zoom, ShareBasemap.maxZoom)
        // One point (a degenerate segment) is not a crash.
        XCTAssertEqual(try XCTUnwrap(ShareBasemap.frame([path([(10, 20), (10, 20)])])).zoom, ShareBasemap.maxZoom)
        XCTAssertNil(ShareBasemap.frame([]))
    }

    @MainActor
    func testTheCardPaintsTheMapUnderTheLineAndKeepsItsSize() throws {
        Theme.registerFonts()
        let d = try XCTUnwrap(Fixture.decode(ActivityResponse.self, "activity").drives.first { $0.id == "drv-3" })
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "America/Chicago")!
        cal.locale = Locale(identifier: "en_US")
        let segs = ["_p~iF~ps|U_ulLnnqC_mqNvxq`@"]
        let card = MoneyShareCard.trip(d, includeCost: false, routeSegments: segs, calendar: cal, metric: false)
        let size = ShareBasemap.size
        let fmt = UIGraphicsImageRendererFormat()
        fmt.scale = 2
        let image = UIGraphicsImageRenderer(size: size, format: fmt).image { c in
            UIColor(red: 0.1, green: 0.25, blue: 0.45, alpha: 1).setFill()
            c.fill(CGRect(origin: .zero, size: size))
        }
        let pic = ShareBasemap.Picture(image: image, lines: [[CGPoint(x: 80, y: 220), CGPoint(x: 400, y: 80), CGPoint(x: 800, y: 140)]], size: size)
        let plain = try XCTUnwrap(MoneyShareCardCanvas.png(card))
        let mapped = try XCTUnwrap(MoneyShareCardCanvas.png(card, basemap: pic))
        XCTAssertEqual(try XCTUnwrap(UIImage(data: mapped)).size, CGSize(width: 1080, height: 1080))
        XCTAssertNotEqual(plain, mapped)
        // PNG chunks: pixels only, nothing that names a place (no text chunk).
        for tag in ["tEXt", "iTXt", "zTXt"] { XCTAssertNil(mapped.range(of: Data(tag.utf8)), tag) }
        let a = XCTAttachment(data: mapped, uniformTypeIdentifier: "public.png")
        a.name = "share-card-synthetic-basemap.png"
        a.lifetime = .keepAlways
        add(a)
    }

    /// Real tiles from a server's public tile archive (not part of the suite unless SHARE_BASEMAP_DUMP names a directory and
    /// SHARE_BASEMAP_SERVER the server): the exported cards are the ones reviewed.
    @MainActor
    func testRealTilesRenderUnderTheLine() async throws {
        guard let dir = ProcessInfo.processInfo.environment["SHARE_BASEMAP_DUMP"], !dir.isEmpty,
              let host = ProcessInfo.processInfo.environment["SHARE_BASEMAP_SERVER"], let real = URL(string: host) else {
            throw XCTSkip("SHARE_BASEMAP_DUMP not set")
        }
        Theme.registerFonts()
        let d = try XCTUnwrap(Fixture.decode(ActivityResponse.self, "activity").drives.first { $0.id == "drv-3" })
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "America/Chicago")!
        cal.locale = Locale(identifier: "en_US")
        // Made-up lines over a real area of the map (the Front Range), in two stretches so the gap shows; none is a real drive.
        func bend(_ i: Int) -> (Double, Double) {
            let t = Double(i) / 120
            let lat: Double = 39.2 + t * 0.9 + 0.05 * sin(t * 9)
            let lon: Double = -105.2 + t * 0.8 + 0.06 * cos(t * 7)
            return (lat, lon)
        }
        func short(_ i: Int) -> (Double, Double) {
            let k = Double(i)
            let lat: Double = 39.74 + k * 0.00018
            let lon: Double = -104.99 + k * 0.00026 + 0.0004 * sin(k / 5)
            return (lat, lon)
        }
        let cases: [(String, [String])] = [
            ("short", [Self.encode6((0...40).map(short))]),
            ("long", [Self.encode6((0...120).map(bend))]),
            ("gap", [Self.encode6((0...50).map(bend)), Self.encode6((70...120).map(bend))]),
        ]
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        for (name, segs) in cases {
            let pic = await ShareBasemap.render(segments: segs, server: real, token: nil)
            XCTAssertNotNil(pic, name)
            XCTAssertEqual(pic?.lines.count, segs.count, "each segment stays its own line")
            let card = MoneyShareCard.trip(d, includeCost: false, routeSegments: segs, calendar: cal, metric: false)
            let png = try XCTUnwrap(MoneyShareCardCanvas.png(card, basemap: pic))
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("ios-share-basemap-\(name).png"))
        }
        // A server that cannot be reached: no picture, so the card keeps the plain line.
        let none = await ShareBasemap.render(segments: cases[1].1, server: URL(string: "http://127.0.0.1:9")!, token: nil)
        XCTAssertNil(none)
    }

    private static func encode6(_ pts: [(Double, Double)]) -> String {
        var out = "", pl = 0, pn = 0
        func put(_ v: Int) {
            var x = v < 0 ? ~(v << 1) : v << 1
            while x >= 0x20 { out.append(Character(UnicodeScalar(UInt8((0x20 | (x & 0x1f)) + 63)))); x >>= 5 }
            out.append(Character(UnicodeScalar(UInt8(x + 63))))
        }
        for (a, o) in pts {
            let la = Int((a * 1e6).rounded()), lo = Int((o * 1e6).rounded())
            put(la - pl); put(lo - pn); pl = la; pn = lo
        }
        return out
    }
}
