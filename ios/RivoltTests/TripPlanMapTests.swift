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

extension TripPlanMapTests {
    /// The plan as Rivian sends it: upper-case waypoint types, "DC_CHARGE_STATION" for a charging stop, an "OTHER" via. (Synthetic
    /// values; the structure is the web's `isChargingWaypoint` input.)
    private func rivianPlan() throws -> TripPlan { try Fixture.decode(TripPlan.self, "trip_plan_rivian_shape") }

    func testARealPlansChargersAreFoundWhateverTheCaseOrTheStationWord() throws {
        let route = try XCTUnwrap(try rivianPlan().best)
        XCTAssertEqual(route.chargingStops.map(\.name), ["Sample Fast Charge A", "Sample Fast Charge B", "Sample Fast Charge C"],
                       "ORIGIN, DESTINATION and OTHER are not stops; DC_CHARGE_STATION is")
        XCTAssertEqual(TripPlanLogic.stopsTitle(route), "3 charging stops")
    }

    func testARealPlansMapHasAPinPerStopAndTheListHasTheSameStops() throws {
        let route = try XCTUnwrap(try rivianPlan().best)
        let drawing = try XCTUnwrap(PlanRouteLogic.drawing(route: route, line: PlanRouteLogic.places(route)))
        XCTAssertEqual(drawing.stops.count, route.chargingStops.count)
        XCTAssertEqual(drawing.stops.map(\.label), ["to 78% · 40 min", "to 81% · 46 min", "to 75% · 52 min"])
    }

    func testAPlanThatChargesNeverSaysNoChargingIsNeeded() throws {
        var route = try XCTUnwrap(try rivianPlan().best)
        route.waypoints = route.waypoints.filter { !$0.isCharger }
        XCTAssertTrue(route.chargingStops.isEmpty)
        XCTAssertEqual(TripPlanLogic.stopsTitle(route), "Charging")
        XCTAssertEqual(TripPlanLogic.noStopsLine(route), "This plan charges for about 2 h 18 min, but its stops aren't listed here. Open it on the web to see them.")
        route.totalChargingDurationSec = 0
        XCTAssertEqual(TripPlanLogic.stopsTitle(route), "No charging stop needed")
    }

    func testTheSleepSearchRunsAlongTheRoadLineWhenThereIsOne() throws {
        let route = try XCTUnwrap(try rivianPlan().best)
        let road = (0..<5000).map { RouteSnap.Coord(lat: 45.5 - Double($0) * 0.001, lon: -122.6) }
        let along = TripPlanLogic.overnightRequest(route, roadLine: road)
        XCTAssertEqual(along.path.count, 600, "thinned")
        XCTAssertEqual(along.path.first, [45.5, -122.6])
        XCTAssertEqual(along.path.last?.first ?? 0, 45.5 - 4999 * 0.001, accuracy: 1e-9, "both ends kept")
        XCTAssertEqual(TripPlanLogic.overnightRequest(route).path.count, route.waypoints.count, "no road line: the plan's waypoints, as the web does")
    }
}

extension TripPlanMapTests {
    func testEachStopsLegIsTheDistanceSinceTheLastStopAndTheLastLegRunsToTheDestination() throws {
        let route = try XCTUnwrap(try Fixture.decode(TripPlan.self, "trip_plan_rivian_shape").best)
        let legs = TripPlanLogic.legs(route)
        XCTAssertEqual(legs.stops, [290_000, 400_000, 310_000], "290 000, then 690 000 and 1 000 000 m from the start; the OTHER via is not a stop")
        XCTAssertEqual(legs.last, 530_000)
        XCTAssertEqual(TripPlanLogic.legLine(legs.stops[0], first: true, metric: false), "180 mi from the start")
        XCTAssertEqual(TripPlanLogic.legLine(legs.stops[1], first: false, metric: false), "250 mi from the last stop")
        XCTAssertNil(TripPlanLogic.legLine(nil, first: false, metric: false))
    }

    func testAStopsPriceIsAnEstimateOrSaysThePriceIsAtTheCharger() throws {
        let plan = try Fixture.decode(TripPlan.self, "trip_plan")
        let cost = plan.costs?.first
        XCTAssertEqual(TripPlanLogic.stopCostLine(cost, index: 0), "About $11.50 to $14.00 (estimate)")
        XCTAssertNil(TripPlanLogic.stopCostLine(cost, index: 3), "no such stop")
        XCTAssertNil(TripPlanLogic.stopCostLine(nil, index: 0))
        var unpriced = try XCTUnwrap(cost)
        unpriced.breakdown = [.init(networkName: nil, chargerName: "x", energyKwh: 40, costLow: nil, costHigh: nil)]
        XCTAssertEqual(TripPlanLogic.stopCostLine(unpriced, index: 0), "Price shown at the charger.")
    }

    func testEachDaysPinsStartWhereTheDaysBeforeEndedSoARowAndAPinMeetOnOneNumber() throws {
        let trip = try Fixture.decode(TripMultiday.self, "trip_plan_multiday")
        let offsets = PlanRouteLogic.pinOffsets(trip.days)
        XCTAssertEqual(offsets.count, trip.days.count)
        XCTAssertEqual(offsets[0], 0)
        let first = PlanRouteLogic.dayPins(trip.days[0])
        XCTAssertEqual(offsets[1], first.stops.count + (first.night ? 1 : 0), "stops then the night")
        let lines = trip.days.map { _ in [RouteSnap.Coord(lat: 45.5, lon: -122.6), RouteSnap.Coord(lat: 45.9, lon: -121.0)] }
        let drawing = try XCTUnwrap(PlanRouteLogic.drawing(days: trip.days, lines: lines))
        let total = trip.days.map { PlanRouteLogic.dayPins($0) }.reduce(0) { $0 + $1.stops.count + ($1.night ? 1 : 0) }
        XCTAssertEqual(drawing.stops.count, total, "the drawing numbers its pins the way the rows do")
        XCTAssertTrue(drawing.stops[first.stops.count].label.hasPrefix("Night"), "the night follows the day's stops")
    }

    func testPickingAStopOnlyMovesTheRingNotTheDrawing() throws {
        let route = try XCTUnwrap(try Fixture.decode(TripPlan.self, "trip_plan_rivian_shape").best)
        var d = try XCTUnwrap(PlanRouteLogic.drawing(route: route, line: PlanRouteLogic.places(route)))
        XCTAssertNil(d.focus)
        var picked = d
        picked.focus = 1
        XCTAssertNotEqual(d, picked)
        d.focus = 1
        XCTAssertEqual(d, picked)
    }
}

private extension TripWaypoint {
    static var bare: TripWaypoint { try! JSON.decoder().decode(TripWaypoint.self, from: Data("{}".utf8)) }
}
