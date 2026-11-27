import XCTest
@testable import Rivolt

@MainActor
final class ForecastRadarTests: XCTestCase {
    private let server = URL(string: "https://preview.rivolt.dev")!
    private typealias Logic = ForecastRadarLogic

    private func hours(_ pairs: [(Int, Int)]) -> [ForecastHours.Hour] { pairs.map { ForecastHours.Hour(fh: $0.0, validUnix: $0.1) } }

    // MARK: Wire

    func testTheHoursDecodeFromTheServersOwnKeys() throws {
        let json = #"{"cycle":"2026112712","credit":"NOAA RRFS","bounds":[-135,20,-60,55],"hours":[{"fh":1,"valid_unix":1795003200},{"fh":2,"valid_unix":1795006800}],"horizon_unix":1795006800}"#
        let h = try JSON.decoder().decode(ForecastHours.self, from: Data(json.utf8))
        XCTAssertEqual(h.cycle, "2026112712")
        XCTAssertEqual(h.hours.map(\.fh), [1, 2])
        XCTAssertEqual(h.hours[1].validUnix, 1795006800)
        XCTAssertEqual(h.bounds, [-135, 20, -60, 55])
    }

    func testTheForecastTileTemplateCarriesTheCycleAndHour() {
        let l = RadarLayer.forecast(server: server, cycle: "2026112712", fh: 7)
        XCTAssertEqual(l.tileTemplate, "https://preview.rivolt.dev/api/maps/forecast-radar/2026112712/7/{z}/{x}/{y}.png")
        XCTAssertEqual(l.maxZoom, 7)
        // The live frame is what it was.
        XCTAssertEqual(RadarLayer(server: server, frameUnix: 42).tileTemplate, "https://preview.rivolt.dev/api/maps/radar/{z}/{x}/{y}.png?t=42")
        XCTAssertNotEqual(l, RadarLayer.forecast(server: server, cycle: "2026112712", fh: 8))
    }

    // MARK: Slots, lines, label

    func testSlotsRunFromTheDepartureHourToTheArrivalHour() {
        let hs = hours([(1, 3600 * 10), (2, 3600 * 11), (3, 3600 * 12)])
        // Leaves 10:20, arrives 12:40: the hours 10, 11, 12 (the departure's own hour, floored, not the next).
        let s = Logic.slots(hours: hs, depart: 3600 * 10 + 1200, arrive: 3600 * 12 + 2400)
        XCTAssertEqual(s, [.init(t: 3600 * 10, fh: 1), .init(t: 3600 * 11, fh: 2), .init(t: 3600 * 12, fh: 3)])
        // Past the horizon and before the first hour have no forecast hour; a trip inside one hour still has its one slot.
        let past = Logic.slots(hours: hs, depart: 3600 * 12, arrive: 3600 * 14)
        XCTAssertEqual(past.map(\.fh), [3, nil, nil])
        XCTAssertEqual(Logic.slots(hours: hs, depart: 3600 * 9, arrive: 3600 * 9 + 5).map(\.fh), [nil])
        XCTAssertEqual(Logic.slots(hours: hs, depart: 0, arrive: 100), [])
    }

    func testAtMostFortyEightSlots() {
        XCTAssertEqual(Logic.slots(hours: [], depart: 3600, arrive: 3600 * 200).count, 48)
    }

    func testTheLinesSayWhyNothingIsDrawn() {
        XCTAssertEqual(Logic.line(slot: .init(t: 0, fh: 3), inside: true), nil)
        XCTAssertEqual(Logic.line(slot: .init(t: 0, fh: nil), inside: true), "The forecast doesn't reach this hour.")
        XCTAssertEqual(Logic.line(slot: nil, inside: true), "The forecast doesn't reach this hour.")
        XCTAssertEqual(Logic.line(slot: .init(t: 0, fh: 3), inside: false), "The forecast covers the contiguous US.")
        XCTAssertTrue(Logic.inBounds([-135, 20, -60, 55], lat: 40, lon: -100))
        XCTAssertFalse(Logic.inBounds([-135, 20, -60, 55], lat: 52, lon: -2))
        XCTAssertFalse(Logic.inBounds([], lat: 40, lon: -100))
    }

    func testTheLabelIsTheHourInThePhonesZone() {
        let utc = TimeZone(identifier: "UTC")!, us = Locale(identifier: "en_US")
        // 2026-11-27 15:00 UTC.
        XCTAssertEqual(Logic.label(unix: 1_795_791_600, zone: utc, locale: us), "Forecast for 3 pm")
        XCTAssertEqual(Logic.hour(unix: 1_795_791_600, zone: utc, locale: us), "3 pm")
        XCTAssertEqual(Logic.caption(credit: nil, unix: 1_795_791_600, zone: utc, locale: us), "NOAA RRFS · Forecast for 3 pm")
        XCTAssertTrue(Logic.spoken(credit: "NOAA RRFS", unix: 1_795_791_600, zone: utc, locale: us).hasPrefix("light, moderate, heavy, NOAA RRFS radar, "))
    }

    // MARK: Where the truck is

    private func iso(_ t: Int) -> String { ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: TimeInterval(t))) }

    private func route(_ waypoints: [(lat: Double, lon: Double, arrive: Int, depart: Int, dist: Double)], total: Double) throws -> TripRoute {
        let w = waypoints.map { #"{"Latitude":\#($0.lat),"Longitude":\#($0.lon),"ArrivalTimeUTC":"\#(iso($0.arrive))","DepartureTimeUTC":"\#(iso($0.depart))","DistanceFromOriginMeters":\#($0.dist)}"# }
        return try JSON.decoder().decode(TripRoute.self, from: Data(#"{"TotalDriveDistanceMeters":\#(total),"Waypoints":[\#(w.joined(separator: ","))]}"#.utf8))
    }

    /// The web's tripPure tests read the same file: one table, two languages.
    func testPositionAtMatchesTheWebsSharedVectors() throws {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("web/src/map/testdata/position-at.json")
        let data = try Data(contentsOf: url)
        let table = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let plan = try XCTUnwrap(table["plan"] as? [String: Any])
        let wps = try XCTUnwrap(plan["waypoints"] as? [[String: Double]])
        let r = try route(wps.map { (lat: $0["lat"]!, lon: $0["lon"]!, arrive: Int($0["arrive"]!), depart: Int($0["depart"]!), dist: $0["distance"]!) }, total: wps.last!["distance"]!)
        let cases = try XCTUnwrap(table["cases"] as? [[String: Any]])
        XCTAssertGreaterThanOrEqual(cases.count, 9)
        for c in cases {
            let name = c["name"] as? String ?? "?"
            let got = Logic.positionAt(r, line: [], t: (c["t"] as? Double) ?? 0)
            if let want = c["want"] as? [Double] {
                let g = try XCTUnwrap(got, name)
                XCTAssertEqual(g.lat, want[0], accuracy: 1e-9, name)
                XCTAssertEqual(g.lon, want[1], accuracy: 1e-9, name)
            } else {
                XCTAssertNil(got, name)
            }
        }
    }

    func testTheTruckFollowsTheRoadLineNotTheStraightOne() throws {
        // Two stops 100 km apart by road, which runs east along lat 40 and then north; the straight line between them is diagonal.
        let r = try route([(40, -100, 1000, 1000, 0), (41, -98.8, 2000, 2000, 100_000)], total: 100_000)
        let line = [RouteSnap.Coord(lat: 40, lon: -100), RouteSnap.Coord(lat: 40, lon: -98.8), RouteSnap.Coord(lat: 41, lon: -98.8)]
        let on = try XCTUnwrap(Logic.positionAt(r, line: line, t: 1250))
        XCTAssertEqual(on.lat, 40, accuracy: 1e-6, "a quarter of the way is still on the first stretch of the road")
        XCTAssertGreaterThan(on.lon, -100)
        let straight = try XCTUnwrap(Logic.positionAt(r, line: [], t: 1250))
        XCTAssertGreaterThan(straight.lat, 40.2, "without a road line it is the straight line")
        // A stop whose distance is missing (legacy plan): the straight line, never a guess along the road.
        let legacy = try route([(40, -100, 1000, 1000, 0), (41, -98.8, 2000, 2000, 0)], total: 100_000)
        XCTAssertEqual(try XCTUnwrap(Logic.positionAt(legacy, line: line, t: 1250)).lat, 40.25, accuracy: 1e-9)
    }

    func testTripTimesAreTheOriginsDepartureAndTheDestinationsArrival() throws {
        let r = try route([(40, -100, 900, 1000, 0), (41, -100, 2000, 2600, 1), (42, -100, 4600, 4700, 2)], total: 2)
        XCTAssertEqual(Logic.tripTimes(r).depart, 1000)
        XCTAssertEqual(Logic.tripTimes(r).arrive, 4600)
        XCTAssertEqual(Logic.centre(r)?.lat ?? 0, 41, accuracy: 1e-9)
    }

    // MARK: The model

    private final class Source: ForecastSource, @unchecked Sendable {
        var answers: [Result<ForecastHours, Error>] = []
        private(set) var calls = 0
        func forecastHours() async throws -> ForecastHours {
            calls += 1
            return try (answers.isEmpty ? .failure(APIError.server(status: 503, message: "")) : answers.removeFirst()).get()
        }
    }

    private func forecastHours(cycle: String = "c1") -> ForecastHours { ForecastHours(cycle: cycle, credit: "NOAA RRFS", bounds: [-135, 20, -60, 55], hours: hours([(1, 3600), (2, 7200)]), horizonUnix: 7200) }

    func testWhatTheServerSaysDecidesTheMode() async {
        for (status, want) in [(402, ForecastRadarModel.Load.hidden), (401, .hidden), (403, .hidden), (404, .hidden), (429, .hidden), (503, .down), (500, .down)] {
            let m = ForecastRadarModel(), s = Source()
            s.answers = [.failure(APIError.server(status: status, message: ""))]
            await m.fetch(source: s)
            XCTAssertEqual(m.load, want, "HTTP \(status)")
        }
        let m = ForecastRadarModel(), s = Source()
        s.answers = [.success(forecastHours()), .failure(URLError(.notConnectedToInternet))]
        await m.fetch(source: s)
        XCTAssertEqual(m.hours?.cycle, "c1")
        await m.fetch(source: s)
        XCTAssertEqual(m.load, .down, "a refresh that fails says so: tiles answer 503 too, an old hour label over no rain would look dry")
        XCTAssertNil(m.hours)
        XCTAssertNil(m.layer(server: server, slots: [Logic.Slot(t: 3600, fh: 1)], inside: true), "no layer while down")
        XCTAssertEqual(Logic.lineDown, "Forecast unavailable")
        s.answers = [.success(forecastHours(cycle: "c2"))]
        await m.fetch(source: s)
        XCTAssertEqual(m.hours?.cycle, "c2", "it recovers on the next ask")
    }

    func testWhileDownItAsksAgainSoonerThanWhenReady() async throws {
        let m = ForecastRadarModel(), s = Source()
        m.refreshEvery = .seconds(60)
        m.retryDown = .milliseconds(20)
        s.answers = [.failure(URLError(.timedOut)), .success(forecastHours())]
        await m.fetch(source: s)
        XCTAssertEqual(m.load, .down)
        let t = Task { await m.keepFresh(source: s) }
        try await Task.sleep(for: .milliseconds(300))
        t.cancel()
        XCTAssertEqual(s.calls, 2)
        XCTAssertNotNil(m.hours)
    }

    func testTheForecastReplacesTheLiveRadarOnlyWhereItIsOffered() {
        typealias M = ForecastRadarModel
        XCTAssertTrue(M.replacesLive(keyOn: true, demo: false, radarOn: true, load: .loading))
        XCTAssertTrue(M.replacesLive(keyOn: true, demo: false, radarOn: true, load: .down))
        XCTAssertFalse(M.replacesLive(keyOn: false, demo: false, radarOn: true, load: .loading), "key off")
        XCTAssertFalse(M.replacesLive(keyOn: true, demo: true, radarOn: true, load: .loading), "demo")
        XCTAssertFalse(M.replacesLive(keyOn: true, demo: false, radarOn: false, load: .loading), "Radar button off")
        XCTAssertFalse(M.replacesLive(keyOn: true, demo: false, radarOn: true, load: .hidden), "a free account keeps the live radar")
    }

    func testASweepFasterThanTheDebounceNeverAsksForTheHoursBetween() async throws {
        let m = ForecastRadarModel()
        m.debounce = .milliseconds(150)
        var seen = Set<Int>()
        for i in 1...5 {
            m.select(i)
            XCTAssertEqual(m.index, i, "the label follows the thumb at once")
            seen.insert(m.drawnIndex)
            try await Task.sleep(for: .milliseconds(40))
            seen.insert(m.drawnIndex)
        }
        XCTAssertEqual(seen, [0], "nothing is drawn while the thumb is moving")
        try await Task.sleep(for: .milliseconds(400))
        XCTAssertEqual(m.drawnIndex, 5, "it settles on the hour the thumb stopped on")
    }

    func testTheLayerIsTheSettledHoursOrNothing() async {
        let m = ForecastRadarModel(), s = Source()
        s.answers = [.success(forecastHours())]
        await m.fetch(source: s)
        let slots = [Logic.Slot(t: 3600, fh: 1), .init(t: 7200, fh: 2), .init(t: 10800, fh: nil)]
        XCTAssertEqual(m.layer(server: server, slots: slots, inside: true)?.forecast, .init(cycle: "c1", fh: 1))
        XCTAssertNil(m.layer(server: server, slots: slots, inside: false), "outside the grid")
        m.debounce = .milliseconds(5)
        m.select(2)
        try? await Task.sleep(for: .milliseconds(100))
        XCTAssertNil(m.layer(server: server, slots: slots, inside: true), "past the horizon")
        m.restart()
        XCTAssertEqual(m.drawnIndex, 0)
        XCTAssertNil(ForecastRadarModel().layer(server: server, slots: slots, inside: true), "hours not loaded")
    }
}
