import XCTest
@testable import Rivolt

private final class RoadsFake: RouteSnapSource, @unchecked Sendable {
    var shape: String?
    private(set) var routeRequests: [ValhallaRouteRequest] = []
    func valhallaTrace(_ body: ValhallaTraceRequest) async throws -> ValhallaTraceResponse { throw APIError.server(status: 500, message: "") }
    func valhallaRoute(_ body: ValhallaRouteRequest) async throws -> ValhallaRouteResponse {
        routeRequests.append(body)
        guard let shape else { throw APIError.server(status: 500, message: "") }
        return try JSON.decoder().decode(ValhallaRouteResponse.self, from: Data(#"{"trip":{"legs":[{"shape":"\#(shape)"}]}}"#.utf8))
    }
}

final class TripPlanMapTests: XCTestCase {
    // Three points: 47.6,-122.3 then 47.0,-120.0 then 47.66,-117.43, in Valhalla's precision-6 encoding.
    private let shape = "_wgxyA~drghF~zrc@_ekkC_ahg@_pz{C"

    private func route(polyline: String? = nil) throws -> TripRoute {
        let stored = polyline.map { #""route_polyline":"\#($0)","# } ?? ""
        let json = """
        {\(stored)"DestinationReached":true,"Waypoints":[
        {"WaypointType":"origin","Latitude":47.6,"Longitude":-122.3},
        {"WaypointType":"charger","Name":"Sample Fast Charge","Latitude":47.0,"Longitude":-120.0,"ChargeDurationSec":1500,"ArrivalSoC":31,"DepartureSoC":72},
        {"WaypointType":"destination","Latitude":47.66,"Longitude":-117.43}]}
        """
        return try JSON.decoder().decode(TripRoute.self, from: Data(json.utf8))
    }

    func testAStopsPinSaysWhatToChargeToAndForHowLong() throws {
        let stop = try XCTUnwrap(try route().chargingStops.first)
        XCTAssertEqual(PlanRouteLogic.stopLabel(stop), "to 72% · 25 min")
        XCTAssertEqual(PlanRouteLogic.stopLabel(TripWaypoint.bare), "")
    }

    func testTheRoadRouteHoldsTheStartAndEndAndPassesThroughChargers() async throws {
        let roads = RoadsFake()
        roads.shape = shape
        let r = try route()
        let line = await PlanRouteLogic.line(route: r, source: roads)
        XCTAssertEqual(line.count, 3)
        XCTAssertEqual(line.first?.lat ?? 0, 47.6, accuracy: 1e-5)
        XCTAssertEqual(roads.routeRequests.first?.locations.map(\.type), ["break", "through", "break"])
    }

    func testAStoredShapeIsDrawnWithoutAskingTheRoads() async throws {
        let roads = RoadsFake()
        let line = await PlanRouteLogic.line(route: try route(polyline: shape), source: roads)
        XCTAssertEqual(line.count, 3)
        XCTAssertTrue(roads.routeRequests.isEmpty)
    }

    func testWhenTheRoadsFailTheLineIsStraightLegsBetweenThePlaces() async throws {
        let line = await PlanRouteLogic.line(route: try route(), source: RoadsFake())
        XCTAssertEqual(line.map(\.lat), [47.6, 47.0, 47.66])
    }

    func testTheDrawingHasALinePinsAtTheEndsAndOneLabelledPinPerStop() throws {
        let r = try route()
        let d = try XCTUnwrap(PlanRouteLogic.drawing(route: r, line: PlanRouteLogic.places(r)))
        XCTAssertEqual(d.start?.lat, 47.6)
        XCTAssertEqual(d.end?.lon, -117.43)
        XCTAssertEqual(d.stops.map(\.label), ["to 72% · 25 min"])
        XCTAssertFalse(d.hasSpeed)
        XCTAssertNil(PlanRouteLogic.drawing(route: r, line: []), "nothing to draw without a line")
    }
}

extension TripPlanMapTests {
    func testTwoDaysAreOneDrawingWithAPinForTheNight() throws {
        let trip = try Fixture.decode(TripMultiday.self, "trip_plan_multiday")
        let lines = trip.days.map { _ in [RouteSnap.Coord(lat: 45.5, lon: -122.6), RouteSnap.Coord(lat: 45.9, lon: -121.0)] }
        let d = try XCTUnwrap(PlanRouteLogic.drawing(days: trip.days, lines: lines))
        XCTAssertEqual(d.pieces.count, trip.days.count)
        let nights = d.stops.filter { $0.label.hasPrefix("Night") }
        XCTAssertEqual(nights.count, trip.days.filter { $0.overnight != nil }.count)
        XCTAssertNil(PlanRouteLogic.drawing(days: trip.days, lines: []), "nothing to draw without a line")
    }

    func testTheNightPinNamesTheStayOrJustSaysNight() {
        XCTAssertEqual(PlanRouteLogic.nightLabel(nil, place: nil), "Night")
        let o = try! JSON.decoder().decode(TripMultiday.Overnight.self, from: Data(#"{"name":"Sample Inn"}"#.utf8))
        XCTAssertEqual(PlanRouteLogic.nightLabel(o, place: nil), "Night: Sample Inn")
    }
}

private extension TripWaypoint {
    static var bare: TripWaypoint { try! JSON.decoder().decode(TripWaypoint.self, from: Data("{}".utf8)) }
}
