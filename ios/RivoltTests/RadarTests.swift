import MapLibre
import SwiftUI
import XCTest
@testable import Rivolt

@MainActor
final class RadarTests: XCTestCase {
    private final class Fake: RadarSource, @unchecked Sendable {
        var answers: [Result<RadarFrame, Error>] = []
        var config: Result<MapsConfig, Error> = .success(MapsConfig(tiles: .init(radarUrl: "/api/maps/radar/{z}/{x}/{y}.png", radarFrameUrl: "/api/maps/radar/frame", radarMinzoom: 3, radarMaxzoom: 9)))
        private(set) var configCalls = 0
        private(set) var calls = 0
        func mapsConfig() async throws -> MapsConfig { configCalls += 1; return try config.get() }
        func radarFrame() async throws -> RadarFrame {
            calls += 1
            let r = answers.isEmpty ? .failure(APIError.server(status: 503, message: "")) : answers.removeFirst()
            return try r.get()
        }
    }

    private func frame(_ unix: Int) -> RadarFrame { RadarFrame(frame: "2026-11-09T00:42:00Z", frameUnix: unix, framesUnix: [unix - 600, unix], minZoom: 3, maxZoom: 9) }
    private let server = URL(string: "https://preview.rivolt.dev")!

    func testTheFrameDecodesFromTheServersOwnKeys() throws {
        let json = #"{"frame":"2026-11-09T00:24:11Z","frame_unix":1794266651,"frames_unix":[1794266051,1794266651],"min_zoom":3,"max_zoom":9}"#
        let f = try JSON.decoder().decode(RadarFrame.self, from: Data(json.utf8))
        XCTAssertEqual(f.frameUnix, 1794266651)
        XCTAssertEqual(f.framesUnix?.count, 2)
        XCTAssertEqual(f.maxZoom, 9)
    }

    func testThereIsAButtonOnlyWhereTheServerAdvertisesRadar() async {
        let on = RadarModel(), src = Fake()
        await on.probe(source: src)
        XCTAssertEqual(on.availability, .available)
        // The flag off: the config has no radar keys. A config that cannot be read means no button either, with no error shown.
        for config in [Result<MapsConfig, Error>.success(MapsConfig(tiles: .init())), .success(MapsConfig(tiles: nil)), .failure(URLError(.notConnectedToInternet))] {
            let m = RadarModel(), s = Fake()
            s.config = config
            await m.probe(source: s)
            XCTAssertEqual(m.availability, .unavailable)
            await m.toggle(source: s)
            XCTAssertFalse(m.isOn, "a hidden button cannot turn it on")
            XCTAssertEqual(s.calls, 0, "and no frame is asked for")
        }
    }

    func testTheServerIsAskedOnceNotOnEveryPlan() async {
        let m = RadarModel(), src = Fake()
        await m.probe(source: src)
        await m.probe(source: src)
        XCTAssertEqual(src.configCalls, 1)
        m.reset()
        await m.probe(source: src)
        XCTAssertEqual(src.configCalls, 2, "another server or account asks again")
    }

    func testTheDecodedConfigKeepsTheRadarKeys() throws {
        let json = #"{"tiles":{"url":"/api/maps/tiles/us.pmtiles","radar_url":"/api/maps/radar/{z}/{x}/{y}.png","radar_frame_url":"/api/maps/radar/frame","radar_minzoom":3,"radar_maxzoom":9}}"#
        let c = try JSON.decoder().decode(MapsConfig.self, from: Data(json.utf8))
        XCTAssertTrue(c.hasRadar)
        XCTAssertEqual(c.tiles?.radarMaxzoom, 9)
        XCTAssertFalse(try JSON.decoder().decode(MapsConfig.self, from: Data(#"{"tiles":{"url":"x"}}"#.utf8)).hasRadar)
    }

    func testTheLayerIsOnlyThereWhileTheButtonIsOn() async {
        let m = RadarModel(), src = Fake()
        src.answers = [.success(frame(1794266651))]
        await m.probe(source: src)
        XCTAssertNil(m.layer(server: server), "off to begin with")
        await m.toggle(source: src)
        let layer = m.layer(server: server)
        XCTAssertEqual(layer?.tileTemplate, "https://preview.rivolt.dev/api/maps/radar/{z}/{x}/{y}.png?t=1794266651", "a new frame is a new URL")
        XCTAssertEqual(layer?.minZoom, 3)
        await m.toggle(source: src)
        XCTAssertNil(m.layer(server: server))
    }

    func testANewerFrameIsFetchedWhileOnAndAFeedThatGoesDownTakesTheLayerAwayQuietly() async {
        let m = RadarModel(), src = Fake()
        m.refreshEvery = .milliseconds(10)
        src.answers = [.success(frame(100)), .success(frame(200)), .failure(URLError(.notConnectedToInternet)), .failure(APIError.server(status: 503, message: ""))]
        await m.probe(source: src)
        await m.toggle(source: src)
        await m.keepFresh(source: src)
        XCTAssertFalse(m.isOn, "the feed is down: off")
        XCTAssertEqual(m.availability, .unavailable)
        XCTAssertEqual(m.frame?.frameUnix, 200, "the newer frame was taken, and a blip in between kept it")
        XCTAssertEqual(src.calls, 4)
    }

    func testAReconnecting503StillTakesTheLayerAwayQuietly() async {
        let m = RadarModel(), src = Fake()
        src.answers = [.failure(APIError.server(status: 503, message: "", retryAfter: 5))]
        await m.probe(source: src)
        XCTAssertEqual(m.availability, .available)
        await m.toggle(source: src)
        XCTAssertEqual(m.availability, .unavailable)
        XCTAssertNil(m.layer(server: server))
    }

    func testTheFreshnessLineIsInThePhonesZone() {
        // 2026-11-09 00:42 UTC is 6:42 PM the evening before in Chicago.
        let unix = 1_794_271_320
        // (iOS puts a narrow no-break space before PM.)
        let line = RadarLogic.asOf(unix: unix, zone: TimeZone(identifier: "America/Chicago")!, locale: Locale(identifier: "en_US")).replacingOccurrences(of: "\u{202F}", with: " ")
        XCTAssertEqual(line, "NOAA/NWS radar · 6:42 PM")
    }

    func testTheLegendNamesTheEndsAtAccessibilitySizes() {
        XCTAssertEqual(RadarLogic.legendWords(accessibilitySize: false), ["light", "moderate", "heavy"])
        XCTAssertEqual(RadarLogic.legendWords(accessibilitySize: true), ["light", "heavy"])
    }

    func testTheRadarLayerSitsUnderTheRouteAndGoesWhenTurnedOff() async throws {
        let line = (0..<10).map { RouteSnap.Coord(lat: 40 + Double($0) * 0.01, lon: -100 + Double($0) * 0.01) }
        let d = RouteSnap.Drawing(pieces: [RouteSnap.Piece(coords: line, speed: nil)], routedGaps: [], gaps: [], start: line.first, end: line.last, hasSpeed: false)
        let coord = RouteMapView.Coordinator(drawing: d, onHealth: { _ in })
        let map = MLNMapView.displayOnly(styleJSON: ##"{"version":8,"sources":{},"layers":[{"id":"bg","type":"background","paint":{"background-color":"#101010"}}]}"##, watch: coord, interactive: false)
        for _ in 0..<100 where map.style?.layer(withIdentifier: "rivolt-route-glow") == nil { try await Task.sleep(for: .milliseconds(100)) }
        let style = try XCTUnwrap(map.style)
        XCTAssertNotNil(style.layer(withIdentifier: "rivolt-route-glow"), "the route is on the map")
        coord.setRadar(RadarLayer(server: server, frameUnix: 100), on: map)
        let ids = style.layers.map(\.identifier)
        let radar = try XCTUnwrap(ids.firstIndex(of: RadarLayer.sourceID), "the radar layer is added")
        let firstRoute = try XCTUnwrap(ids.firstIndex { $0.hasPrefix(RouteMapView.sourceID) })
        XCTAssertLessThan(radar, firstRoute, "under the route line, the pins and the pills")
        XCTAssertEqual(ids.firstIndex(of: "bg"), 0)
        coord.setRadar(RadarLayer(server: server, frameUnix: 200), on: map)
        XCTAssertEqual(style.layers.filter { $0.identifier == RadarLayer.sourceID }.count, 1, "a newer frame replaces the older one")
        coord.setRadar(nil, on: map)
        XCTAssertNil(style.layer(withIdentifier: RadarLayer.sourceID))
        XCTAssertNil(style.source(withIdentifier: RadarLayer.sourceID))
    }

    func testTheRadarDefaultFromTheSettingsTurnsItOnOncePerSessionWhereThereIsRadar() async {
        let m = RadarModel(), src = Fake()
        src.answers = [.success(frame(100)), .success(frame(200))]
        await m.applyDefault(on: true, source: src)
        XCTAssertEqual(m.availability, .available)
        XCTAssertTrue(m.isOn, "the setting says radar is on to begin with")
        await m.toggle(source: src)
        XCTAssertFalse(m.isOn, "the person turns it off on the map")
        await m.applyDefault(on: true, source: src)
        XCTAssertFalse(m.isOn, "the next plan does not switch it back on")
        m.reset()
        src.answers = [.success(frame(300))]
        await m.applyDefault(on: true, source: src)
        XCTAssertTrue(m.isOn, "a new session or server applies it again")
    }

    func testTheRadarDefaultChangesNothingWhenOffOrWhereThereIsNoRadar() async {
        let off = RadarModel(), a = Fake()
        a.answers = [.success(frame(100))]
        await off.applyDefault(on: false, source: a)
        XCTAssertEqual(off.availability, .available)
        XCTAssertFalse(off.isOn)
        XCTAssertEqual(a.calls, 0, "no frame asked for when it stays off")
        let none = RadarModel(), b = Fake()
        b.config = .success(MapsConfig(tiles: .init()))
        await none.applyDefault(on: true, source: b)
        XCTAssertEqual(none.availability, .unavailable)
        XCTAssertFalse(none.isOn)
        XCTAssertEqual(b.calls, 0)
    }
}
