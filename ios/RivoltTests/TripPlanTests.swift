import XCTest
@testable import Rivolt

private final class FakeTrips: TripSource, @unchecked Sendable {
    var plan: TripPlan?
    var planError: Error?
    var matches: [PlaceMatch] = []
    var position = TruckPosition(latitude: 47.62, longitude: -122.35)
    private(set) var requests: [TripPlanRequest] = []
    private(set) var searches: [String] = []

    func planTrip(_ body: TripPlanRequest) async throws -> TripPlan {
        requests.append(body)
        if let planError { throw planError }
        return plan ?? TripPlan.empty
    }
    func geocode(_ q: String, count: Int) async throws -> [PlaceMatch] { searches.append(q); return matches }
    func vehicleState(id: String) async throws -> VehicleState {
        try JSON.decoder().decode(VehicleState.self, from: Data(#"{"vehicle_id":"v1","battery_level_pct":64.4,"distance_to_empty":300,"battery_capacity_kwh":135}"#.utf8))
    }
    func truckPosition(id: String) async throws -> TruckPosition { position }
}

private extension TripPlan {
    static var empty: TripPlan { try! JSON.decoder().decode(TripPlan.self, from: Data("{}".utf8)) }
}

private let seattle = PlaceMatch(name: "Seattle", latitude: 47.6, longitude: -122.3, admin1: "Washington", country: "United States")
private let spokane = PlaceMatch(name: "Spokane", latitude: 47.66, longitude: -117.43, admin1: "Washington", country: "United States")

final class TripPlanLogicTests: XCTestCase {
    private func route() throws -> TripRoute { try XCTUnwrap(try Fixture.decode(TripPlan.self, "trip_plan").best) }

    func testTheSummaryTheArrivalAndTheStops() throws {
        let r = try route()
        XCTAssertEqual(TripPlanLogic.summary(r, metric: false), "280 mi · 4 h 30 min driving · 25 min charging")
        XCTAssertEqual(TripPlanLogic.summary(r, metric: true), "450 km · 4 h 30 min driving · 25 min charging")
        let la = TimeZone(identifier: "America/Los_Angeles")!
        // Newer iOS puts a narrow no-break space before "PM".
        let arrival = TripPlanLogic.arrival(r, timeZone: la)?.replacingOccurrences(of: "\u{202F}", with: " ")
        XCTAssertEqual(arrival, "12:55 PM", "the destination's arrival, in the zone asked")
        XCTAssertEqual(TripPlanLogic.stopsTitle(r), "1 charging stop")
        let stop = try XCTUnwrap(r.chargingStops.first)
        XCTAssertEqual(TripPlanLogic.stopDetail(stop), "200 kW · 25 min · 31% to 72%")
        XCTAssertNil(TripPlanLogic.shortfall(r, metric: false), "this route reaches the destination")
    }

    func testAShortfallSaysHowFarShortAndHowToFixIt() throws {
        var r = try route()
        r.destinationReached = false
        r.batteryEmptyToDestMeters = 40_000
        let words = try XCTUnwrap(TripPlanLogic.shortfall(r, metric: false))
        XCTAssertTrue(words.contains("about 25 mi before the destination"), words)
        XCTAssertTrue(words.contains("more charge"))
        r.batteryEmptyToDestMeters = 0
        XCTAssertTrue(try XCTUnwrap(TripPlanLogic.shortfall(r, metric: true)).contains("doesn't reach the destination"))
    }

    func testTheCostIsAlwaysAnEstimateAndNeverClaimsMoreThanItKnows() throws {
        let cost = try XCTUnwrap(try Fixture.decode(TripPlan.self, "trip_plan").costs?.first)
        let line = try XCTUnwrap(TripPlanLogic.costLine(cost))
        XCTAssertTrue(line.contains("$11.50 to $14.00"), line)
        XCTAssertTrue(line.contains("(estimate)"))
        XCTAssertTrue(try XCTUnwrap(TripPlanLogic.homeLine(cost)).contains("(estimate)"))
        var none = cost
        none.pricedStops = 0
        XCTAssertNil(TripPlanLogic.costLine(none), "no priced stop, no figure")
        none.pricedStops = 1
        none.unpricedStops = 2
        XCTAssertTrue(try XCTUnwrap(TripPlanLogic.costLine(none)).contains("left out"))
        XCTAssertNil(TripPlanLogic.costLine(nil))
    }

    func testTheRequestSendsTheTrucksChargeAndTimeOnlyWhenSet() {
        let now = TripPlanLogic.request(vehicleID: "v", origin: (47.6, -122.3), destination: (47.7, -117.4), startingSoc: nil, departAt: nil, packKwh: 135)
        XCTAssertNil(now.startingSoc)
        XCTAssertNil(now.departAt)
        XCTAssertEqual(now.waypoints.map(\.waypointType), ["origin", "destination"])
        let set = TripPlanLogic.request(vehicleID: "v", origin: (47.6, -122.3), destination: (47.7, -117.4), startingSoc: 55, departAt: Date(timeIntervalSince1970: 1_800_000_000), packKwh: nil)
        XCTAssertEqual(set.startingSoc, 55)
        XCTAssertEqual(set.departAt, "2027-01-15T08:00:00Z")
    }

    func testEveryFailureIsInPlainWordsNeverTheServersText() {
        let cases: [(Error, String)] = [
            (APIError.rivianReauth, "Rivian needs to be reconnected. Do that in Rivolt on the web, then plan again."),
            (APIError.planQuota(retryAfterSeconds: 1500), "You've used this hour's planning allowance. Try again in about 25 min."),
            (APIError.planQuota(retryAfterSeconds: nil), "You've used this hour's planning allowance. Try again in a while."),
            (APIError.server(status: 502, message: "rivian gateway at https://x exploded"), "The planner isn't answering right now. Try again in a minute."),
            (APIError.server(status: 409, message: "no_position"), "The truck hasn't reported where it is. Pick a starting place."),
            (APIError.server(status: 404, message: "not found"), "Trip planning isn't turned on for this server."),
            (APIError.server(status: 403, message: "x"), "This server doesn't allow trip planning from the app."),
        ]
        for (e, want) in cases { XCTAssertEqual(TripPlanLogic.failure(e), want) }
        let all = cases.map { TripPlanLogic.failure($0.0) }.joined(separator: " ")
        for banned in ["OK", "Normal", "safe", "secure", "towing", "metered", "exact", "measured", "exploded", "https"] { XCTAssertFalse(all.contains(banned), banned) }
    }
}

@MainActor
final class TripPlanModelTests: XCTestCase {
    private func planned(_ src: FakeTrips, _ m: TripPlanModel) async {
        let v = try! JSON.decoder().decode(Vehicle.self, from: Data(#"{"id":"v1","vin":"TESTVIN0000000001","name":"R1S","model":"R1S"}"#.utf8))
        await m.loadTruck(source: src, vehicles: [v])
        m.destination = spokane
        await m.plan(source: src)
    }

    func testPlanningUsesTheTrucksPositionChargeAndPack() async throws {
        let src = FakeTrips()
        src.plan = try Fixture.decode(TripPlan.self, "trip_plan")
        let m = TripPlanModel()
        await planned(src, m)
        XCTAssertEqual(m.liveSoc, 64.4)
        XCTAssertEqual(m.packKwh, 135)
        let req = try XCTUnwrap(src.requests.first)
        XCTAssertEqual(req.vehicleId, "v1")
        XCTAssertNil(req.startingSoc, "the truck's own charge: the server fills it in")
        XCTAssertEqual(req.packKwh, 135)
        XCTAssertEqual(req.waypoints.first?.latitude, 47.62)
        XCTAssertEqual(req.waypoints.last?.longitude, -117.43)
        XCTAssertNotNil(m.plan?.best)
    }

    func testAPickedStartAndAChosenChargeAreSentInstead() async throws {
        let src = FakeTrips()
        src.plan = try Fixture.decode(TripPlan.self, "trip_plan")
        let m = TripPlanModel()
        m.origin = seattle
        m.useTruckCharge = false
        m.startingSoc = 60
        m.leaveNow = false
        await planned(src, m)
        let req = try XCTUnwrap(src.requests.first)
        XCTAssertEqual(req.waypoints.first?.latitude, 47.6)
        XCTAssertEqual(req.startingSoc, 60)
        XCTAssertNotNil(req.departAt)
    }

    func testNoTruckPositionAsksForAStartingPlaceAndSendsNothing() async {
        let src = FakeTrips()
        src.position = TruckPosition(latitude: 0, longitude: 0)
        let m = TripPlanModel()
        await planned(src, m)
        XCTAssertTrue(src.requests.isEmpty)
        XCTAssertEqual(m.state, .failed("The truck hasn't reported where it is. Pick a starting place."))
    }

    func testARefusedPlanIsSaidAndANoRoutePlanIsSaid() async {
        let src = FakeTrips()
        src.planError = APIError.planQuota(retryAfterSeconds: 600)
        let m = TripPlanModel()
        await planned(src, m)
        XCTAssertEqual(m.state, .failed("You've used this hour's planning allowance. Try again in about 10 min."))
        let none = FakeTrips()
        let m2 = TripPlanModel()
        await planned(none, m2)
        XCTAssertEqual(m2.state, .failed("The planner found no route for that trip."))
    }

    func testAShortSearchIsNotSentAndAMatchListClears() async {
        let src = FakeTrips()
        src.matches = [seattle]
        let m = TripPlanModel()
        await m.search(source: src, query: "se")
        XCTAssertTrue(src.searches.isEmpty)
        await m.search(source: src, query: "  seattle ")
        XCTAssertEqual(src.searches, ["seattle"])
        XCTAssertEqual(m.results.count, 1)
        m.clearSearch()
        XCTAssertTrue(m.results.isEmpty)
    }
}
