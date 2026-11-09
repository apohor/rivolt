import XCTest
@testable import Rivolt

/// The Trip planner settings at work on the planner map: which layers are on to begin with, what the search carries, and that a line that does not start at
/// the trip's origin never hides its first miles.
@MainActor
final class PlannerApplyTests: XCTestCase {
    private final class Fake: CorridorLayersSource, PlannerPrefsSource, @unchecked Sendable {
        var prefs: Result<PlannerPrefs, Error> = .failure(APIError.server(status: 404, message: ""))
        private(set) var prefsCalls = 0
        private(set) var requests: [CorridorLayersRequest] = []
        func plannerPrefs() async throws -> PlannerPrefs { prefsCalls += 1; return try prefs.get() }
        func corridorLayers(_ body: CorridorLayersRequest) async throws -> CorridorLayers { requests.append(body); return CorridorLayers() }
    }

    private func prefs(corridor: String = "normal", start: Int = 0, kw: Int = 0, layers: [String] = ["dcfc"], preferred: Bool = false) -> PlannerPrefs {
        PlannerPrefs(driveMode: "EVERYDAY", hasAdapter: true, corridor: corridor, startExcludeMi: start, minFastKw: kw, layersOn: layers, preferredOnly: preferred, weatherOn: true, radarOn: false)
    }

    private func plannedModel() throws -> TripPlanModel {
        let m = TripPlanModel()
        let from = PlaceMatch(name: "Example Origin", latitude: 45.52, longitude: -122.68, admin1: nil, country: nil)
        let to = PlaceMatch(name: "Example Destination", latitude: 38.58, longitude: -121.49, admin1: nil, country: nil)
        m.open(SavedTripLogic.Restored(origin: from, destination: to, plan: try Fixture.decode(TripPlan.self, "trip_plan_rivian_shape"), multiday: nil, stay: nil))
        return m
    }

    func testTheSearchCarriesTheSettingsAndANewServerOrAccountWithNoneCarriesNone() throws {
        let m = try XCTUnwrap(PlannerMapSettings(prefs(corridor: "wide", start: 25, kw: 150)))
        let r = LayerSearch.request(path: [[1, 2], [3, 4]], kinds: ["dcfc"], settings: m, startsAtOrigin: true)
        XCTAssertEqual(r.preset, "wide")
        XCTAssertEqual(r.startExcludeMi, 25)
        XCTAssertEqual(r.minFastKw, 150)
        let none = LayerSearch.request(path: [[1, 2], [3, 4]], kinds: ["dcfc"], settings: nil, startsAtOrigin: true)
        XCTAssertNil(none.preset); XCTAssertNil(none.startExcludeMi); XCTAssertNil(none.minFastKw)
        let enc = JSONEncoder(); enc.keyEncodingStrategy = .convertToSnakeCase; enc.outputFormatting = [.sortedKeys]
        XCTAssertEqual(String(data: try enc.encode(r), encoding: .utf8), #"{"kinds":["dcfc"],"min_fast_kw":150,"path":[[1,2],[3,4]],"preset":"wide","start_exclude_mi":25}"#)
        XCTAssertEqual(String(data: try enc.encode(none), encoding: .utf8), #"{"kinds":["dcfc"],"path":[[1,2],[3,4]]}"#, "nothing extra for a server without the settings")
    }

    func testALineThatDoesNotStartAtTheOriginNeverHidesItsFirstMiles() throws {
        let m = try XCTUnwrap(PlannerMapSettings(prefs(start: 50)))
        XCTAssertEqual(LayerSearch.request(path: [], kinds: [], settings: m, startsAtOrigin: true).startExcludeMi, 50, "the whole trip or Day 1 from the origin")
        XCTAssertEqual(LayerSearch.request(path: [], kinds: [], settings: m, startsAtOrigin: false).startExcludeMi, 0, "Day 2 and later start at an overnight stop")
        XCTAssertTrue(PlanRouteLogic.layerLineStartsAtOrigin(day: nil))
        XCTAssertTrue(PlanRouteLogic.layerLineStartsAtOrigin(day: 1))
        XCTAssertFalse(PlanRouteLogic.layerLineStartsAtOrigin(day: 2))
        XCTAssertFalse(PlanRouteLogic.layerLineStartsAtOrigin(day: 3))
    }

    func testAReplanFromTheTrucksPositionNeverHidesItsFirstMiles() throws {
        XCTAssertTrue(LayerSearch.startsAtOrigin(day: nil))
        XCTAssertTrue(LayerSearch.startsAtOrigin(day: 1))
        XCTAssertFalse(LayerSearch.startsAtOrigin(day: 2))
        XCTAssertFalse(LayerSearch.startsAtOrigin(day: nil, replanFromTruck: true), "a re-plan from the truck's position partway through a trip: you are not leaving charged")
        XCTAssertFalse(LayerSearch.startsAtOrigin(day: 1, replanFromTruck: true))
        let m = try XCTUnwrap(PlannerMapSettings(prefs(start: 50)))
        XCTAssertEqual(LayerSearch.request(path: [], kinds: [], settings: m, startsAtOrigin: LayerSearch.startsAtOrigin(day: nil, replanFromTruck: true)).startExcludeMi, 0)
    }

    func testTheModelAsksForTheSettingsOnceAndSendsThemOnTheSearch() async throws {
        let m = try plannedModel(), src = Fake()
        src.prefs = .success(prefs(corridor: "close", start: 25, kw: 50, layers: ["dcfc", "hotels"]))
        await m.prepareLayers(source: src)
        await m.prepareLayers(source: src)
        XCTAssertEqual(src.prefsCalls, 1)
        XCTAssertEqual(m.layers, [.chargers, .hotels], "the layers that are on to begin with come from the settings")
        let r = try XCTUnwrap(src.requests.first)
        XCTAssertEqual(r.kinds, ["dcfc", "hotels"])
        XCTAssertEqual(r.preset, "close")
        XCTAssertEqual(r.startExcludeMi, 25)
        XCTAssertEqual(r.minFastKw, 50)
    }

    func testAnEmptyChoiceOfLayersMeansNoPinsAndNoSearch() async throws {
        let m = try plannedModel(), src = Fake()
        src.prefs = .success(prefs(layers: []))
        await m.prepareLayers(source: src)
        XCTAssertTrue(m.layers.isEmpty)
        XCTAssertTrue(src.requests.isEmpty)
        XCTAssertTrue(m.markers.isEmpty)
    }

    func testWithoutTheSettingsFastIsOnAndTheSearchCarriesNothingExtra() async throws {
        for failure: Result<PlannerPrefs, Error> in [.failure(APIError.server(status: 404, message: "")), .failure(URLError(.notConnectedToInternet)), .success(PlannerPrefs(driveMode: "EVERYDAY", hasAdapter: nil))] {
            let m = try plannedModel(), src = Fake()
            src.prefs = failure
            await m.prepareLayers(source: src)
            XCTAssertEqual(m.layers, [.chargers])
            let r = try XCTUnwrap(src.requests.first)
            XCTAssertNil(r.preset); XCTAssertNil(r.startExcludeMi); XCTAssertNil(r.minFastKw)
        }
    }

    func testPreferredOnlyFromTheSettingsTakesHoldOnceTheNetworksAreKnownAndOnlyOnce() async throws {
        let m = try plannedModel(), src = Fake()
        src.prefs = .success(prefs(preferred: true))
        await m.prepareLayers(source: src)
        XCTAssertFalse(m.preferredOnly, "no networks known yet: nothing to prefer")
        m.setPreferred(["ea"])
        XCTAssertTrue(m.preferredOnly)
        m.setPreferredOnly(false)
        m.setPreferred(["ea", "evgo"])
        XCTAssertFalse(m.preferredOnly, "a change made on the map stays")
    }

    func testADaysLineSearchDoesNotHideItsFirstMiles() async throws {
        let m = try plannedModel(), src = Fake()
        src.prefs = .success(prefs(start: 50))
        await m.prepareLayers(source: src)
        XCTAssertEqual(src.requests.last?.startExcludeMi, 50, "the plan's own line starts at the origin")
        let line = (0..<20).map { RouteSnap.Coord(lat: 45 + Double($0) * 0.1, lon: -122) }
        await m.setDayLines(line, startsAtOrigin: false, source: src)
        XCTAssertEqual(src.requests.last?.startExcludeMi, 0, "Day 2: it starts at an overnight stop")
        await m.setDayLines(line, startsAtOrigin: true, source: src)
        XCTAssertEqual(src.requests.last?.startExcludeMi, 50)
    }
}
