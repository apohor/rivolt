import XCTest
@testable import Rivolt

private final class FakeTrips: TripSource, @unchecked Sendable {
    var plan: TripPlan?
    var planError: Error?
    var matches: [PlaceMatch] = []
    var position = TruckPosition(latitude: 47.62, longitude: -122.35)
    private(set) var requests: [TripPlanRequest] = []
    private(set) var searches: [String] = []
    var stays = OvernightCandidates()
    var stayError: Error?
    var days: TripMultiday?
    private(set) var stayRequests: [OvernightCandidatesRequest] = []
    private(set) var dayRequests: [TripMultidayRequest] = []

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
    func overnightCandidates(_ body: OvernightCandidatesRequest) async throws -> OvernightCandidates {
        stayRequests.append(body)
        if let stayError { throw stayError }
        return stays
    }
    func planMultiday(_ body: TripMultidayRequest) async throws -> TripMultiday {
        dayRequests.append(body)
        return days ?? TripMultiday()
    }
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
        let arrival = TripPlanLogic.arrival(r, timeZone: la, phoneZone: la)?.replacingOccurrences(of: "\u{202F}", with: " ")
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
        XCTAssertEqual(now.targetArrivalSocPercent, 20, "the web's arrival target")
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

    // MARK: arrival day, low arrival, long drives

    private func routeJSON(drive: Double, soc: Double = 40, depart: String, arrive: String) throws -> TripRoute {
        let json = """
        {"DestinationReached":true,"TotalDriveDurationSec":\(drive),"ArrivalSoC":\(soc),"Waypoints":[
        {"WaypointType":"origin","Latitude":47.6,"Longitude":-122.3,"DepartureTimeUTC":"\(depart)"},
        {"WaypointType":"destination","Latitude":47.66,"Longitude":-117.43,"ArrivalTimeUTC":"\(arrive)"}]}
        """
        return try JSON.decoder().decode(TripRoute.self, from: Data(json.utf8))
    }

    private func plain(_ s: String?) -> String? { s?.replacingOccurrences(of: "\u{202F}", with: " ") }

    func testAnArrivalOnAnotherDayNamesTheDay() throws {
        let la = TimeZone(identifier: "America/Los_Angeles")!
        let sameDay = try routeJSON(drive: 3600, depart: "2026-10-30T15:43:00Z", arrive: "2026-10-30T20:10:00Z")
        XCTAssertEqual(plain(TripPlanLogic.arrival(sameDay, timeZone: la, phoneZone: la)), "1:10 PM")
        let nextDay = try routeJSON(drive: 3600, depart: "2026-10-30T15:43:00Z", arrive: "2026-10-31T09:45:00Z")
        XCTAssertEqual(plain(TripPlanLogic.arrival(nextDay, timeZone: la, phoneZone: la)), "Sat 2:45 AM", "leaves Friday morning, arrives after midnight")
        let nextWeek = try routeJSON(drive: 3600, depart: "2026-10-30T15:43:00Z", arrive: "2026-11-08T09:45:00Z")
        XCTAssertEqual(plain(TripPlanLogic.arrival(nextWeek, timeZone: la, phoneZone: la)), "Nov 8, 1:45 AM")
        let given = TripPlanLogic.arrival(nextDay, departure: ISO8601DateFormatter().date(from: "2026-10-31T01:00:00Z"), timeZone: la, phoneZone: la)
        XCTAssertEqual(plain(given), "Sat 2:45 AM", "a departure from the form beats the plan's own")
    }

    func testALowArrivalChargeGetsTheWebsThresholdAndWords() {
        XCTAssertNil(TripPlanLogic.lowArrival(20))
        XCTAssertNil(TripPlanLogic.lowArrival(nil))
        XCTAssertEqual(TripPlanLogic.lowArrival(8), "That is below the 20% arrival target. Add a charging stop or start with more charge.")
    }

    func testOnlyADriveOverEightHoursIsOfferedInTwoDays() throws {
        XCTAssertFalse(TripPlanLogic.isLongDrive(try routeJSON(drive: 8 * 3600, depart: "2026-10-30T15:00:00Z", arrive: "2026-10-30T23:00:00Z")))
        XCTAssertTrue(TripPlanLogic.isLongDrive(try routeJSON(drive: 8 * 3600 + 1, depart: "2026-10-30T15:00:00Z", arrive: "2026-10-30T23:00:00Z")))
    }

    func testTheTwoDayRequestEndsDayOneAtTheStayAndLeavesTheTruckToTheServer() throws {
        let stay = OvernightCandidates.Candidate(name: "Sample Inn", lat: 45.52, lon: -122.68, l2Kw: 7, l2KwAssumed: true)
        let req = TripPlanLogic.multidayRequest(
            vehicleID: nil, startingSoc: nil, packKwh: nil, startAt: Date(timeIntervalSince1970: 1_800_000_000),
            origin: (47.6, -122.3), destination: (47.7, -117.4), stay: stay, timeZone: TimeZone(identifier: "America/Los_Angeles")!)
        XCTAssertEqual(req.days.count, 2)
        XCTAssertEqual(req.days[0].end, TripPoint(lat: 45.52, lon: -122.68, label: "Sample Inn"))
        XCTAssertEqual(req.days[0].overnight?.l2Kw, 7)
        XCTAssertEqual(req.days[0].overnight?.until, "08:00")
        XCTAssertNil(req.days[1].overnight, "the last day has no night")
        XCTAssertEqual(req.tz, "America/Los_Angeles")
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        let data = try encoder.encode(req)
        let text = String(decoding: data, as: UTF8.self)
        XCTAssertTrue(text.contains("\"start_at\""), text)
        XCTAssertTrue(text.contains("\"l2_kw\":7"), text)
        XCTAssertTrue(text.contains("\"target_arrival_soc_pct\":20"), text)
        XCTAssertFalse(text.contains("vehicle_id"), "left to the server")
    }

    private func longPlan() throws -> TripPlan {
        let route = """
        {"Routes":[{"DestinationReached":true,"TotalDriveDurationSec":57000,"ArrivalSoC":30,"Waypoints":[
        {"WaypointType":"origin","Latitude":47.62,"Longitude":-122.35},{"WaypointType":"destination","Latitude":47.66,"Longitude":-117.43}]}]}
        """
        return try JSON.decoder().decode(TripPlan.self, from: Data(route.utf8))
    }

    func testALongPlanFindsStaysThenPlansTwoDaysAtTheOneChosen() async throws {
        let src = FakeTrips()
        src.plan = try longPlan()
        let stay = OvernightCandidates.Candidate(name: "Sample Inn", lat: 45.52, lon: -122.68, l2Kw: 7)
        src.stays = OvernightCandidates(candidates: [stay], target: .init(reachesEnd: false))
        let m = TripPlanModel()
        await planned(src, m)
        await m.findNight(source: src)
        XCTAssertEqual(m.night, .choose([stay]))
        XCTAssertEqual(src.stayRequests.first?.path.count, 2)
        XCTAssertEqual(src.stayRequests.first?.budgetHours, 8)
        XCTAssertTrue(src.dayRequests.isEmpty, "nothing is planned until a stay is picked")
        src.days = try Fixture.decode(TripMultiday.self, "trip_plan_multiday")
        await m.planNight(source: src, at: stay)
        let req = try XCTUnwrap(src.dayRequests.first)
        XCTAssertEqual(req.origin, TripPoint(lat: 47.62, lon: -122.35))
        XCTAssertEqual(req.days.first?.end.label, "Sample Inn")
        guard case .planned(let trip) = m.night else { return XCTFail("\(m.night)") }
        XCTAssertFalse(trip.days.isEmpty)
        m.clearNight()
        XCTAssertEqual(m.night, .none)
    }

    func testNoStaysOrAWholeTripInOneDayOrARefusalIsSaidInOurOwnWords() async throws {
        let src = FakeTrips()
        src.plan = try longPlan()
        let m = TripPlanModel()
        await planned(src, m)
        await m.findNight(source: src)
        XCTAssertEqual(m.night, .failed("No hotel or campground with a listed charger near where the first day would end. Pick a stop by hand on the web."), "no windows from the server: the plain line")
        src.stays = OvernightCandidates(candidates: [], target: .init(reachesEnd: true))
        await m.findNight(source: src)
        XCTAssertEqual(m.night, .failed("This trip fits in one day."))
        src.stayError = APIError.planQuota(retryAfterSeconds: nil)
        await m.findNight(source: src)
        XCTAssertEqual(m.night, .failed("You've used this hour's planning allowance. Try again in a while."))
    }

    // MARK: the destination's own clock

    func testAnArrivalInAnotherZoneIsOnThatClockAndSaysWhich() throws {
        let la = TimeZone(identifier: "America/Los_Angeles")!, denver = TimeZone(identifier: "America/Denver")!
        // 20:10Z is 1:10 PM in Los Angeles and 2:10 PM in Denver (both on daylight time).
        let r = try routeJSON(drive: 3600, depart: "2026-10-30T15:43:00Z", arrive: "2026-10-30T20:10:00Z")
        XCTAssertEqual(plain(TripPlanLogic.arrival(r, timeZone: denver, phoneZone: la)), "2:10 PM MDT")
        XCTAssertEqual(plain(TripPlanLogic.arrival(r, timeZone: la, phoneZone: la)), "1:10 PM", "the phone's own zone is not named")
        let late = try routeJSON(drive: 3600, depart: "2026-10-30T15:43:00Z", arrive: "2026-10-31T05:45:00Z")
        XCTAssertEqual(plain(TripPlanLogic.arrival(late, timeZone: denver, phoneZone: la)), "11:45 PM MDT",
                       "11:45 PM in Denver is still Friday, the day it left, so no day is named")
    }

    func testAPlansZoneNameIsReadAndAMissingOrUnknownOneKeepsThePhones() throws {
        XCTAssertEqual(TripPlanLogic.zone("America/Denver")?.identifier, "America/Denver")
        XCTAssertNil(TripPlanLogic.zone(nil))
        XCTAssertNil(TripPlanLogic.zone(""))
        XCTAssertNil(TripPlanLogic.zone("Mars/Olympus"))
        let plan = try Fixture.decode(TripPlan.self, "trip_plan")
        XCTAssertEqual(plan.destinationTz, "America/Los_Angeles")
        let trip = try Fixture.decode(TripMultiday.self, "trip_plan_multiday")
        XCTAssertEqual(trip.destinationTz, "America/Los_Angeles")
        XCTAssertEqual(trip.days.first?.endTz, "America/Los_Angeles")
    }

    func testNoStaysSaysHowFarTheSearchLookedAsTheWebDoes() {
        // 10 m/s over the route: 30 min is 18 km, 60 min is 36 km.
        let found = OvernightCandidates(candidates: [], target: .init(reachesEnd: false, alongM: 288_000, windowBeforeMin: 30, windowAfterMin: 60), routeM: 36_000)
        let d = TripPlanLogic.distance
        XCTAssertEqual(TripPlanLogic.noStaysLine(found, driveSec: 3600, metric: true),
                       "No hotel or campground with a listed charger within about \(d(18_000, true)) before or \(d(36_000, true)) after your 8 h point (about \(d(288_000, true)) in). Pick a stop by hand on the web.")
        XCTAssertTrue(TripPlanLogic.noStaysLine(found, driveSec: nil, metric: true).hasPrefix("No hotel or campground with a listed charger near"), "no drive time: the plain line")
    }

    // MARK: nearest stays and hotels with no charger

    private let fallbackAnswer = #"""
    {"candidates":[],"target":{"reaches_end":false,"along_m":288000,"window_before_min":180,"window_after_min":60},"route_m":1530000,
     "nearest":{"before":{"name":"Sample Inn West","lat":44.0,"lon":-121.0,"drive_sec":18000,"offset_sec":-11880},
                "after":{"name":"Sample Inn East","lat":43.0,"lon":-120.0,"drive_sec":35000,"offset_sec":5760}},
     "fallback_stays":[{"name":"Sample Motel","lat":43.5,"lon":-120.5,"id":"n1","kind":"hotel","no_onsite_charger":true,"detour_m":1200,"along_m":300000,
       "fast_charger":{"name":"Sample Fast Charge A","lat":43.51,"lon":-120.49,"entity_id":"e1","distance_m":3200,"max_power_kw":250},
       "source":"openstreetmap","attribution":"© OpenStreetMap contributors"}],
     "attribution":"© OpenStreetMap contributors","attribution_license_url":"https://opendatacommons.org/licenses/odbl/1-0/",
     "attribution_source_url":"https://www.openstreetmap.org/copyright"}
    """#

    private func answer() throws -> OvernightCandidates { try JSON.decoder().decode(OvernightCandidates.self, from: Data(fallbackAnswer.utf8)) }

    func testTheNearestListedStayIsSaidOnEachSide() throws {
        let found = try answer()
        XCTAssertEqual(TripPlanLogic.nearestLines(found), [
            "Nearest listed stay: Sample Inn West, about 3.3 h short of the day (about 5.0 h in).",
            "Nearest listed stay: Sample Inn East, about 1.6 h past the day (about 9.7 h in).",
        ])
        XCTAssertEqual(TripPlanLogic.nearestLines(OvernightCandidates()), [], "an older server sends none")
        XCTAssertTrue(TripPlanLogic.noStaysMessage(found, driveSec: 55_800, metric: false).contains("\nNearest listed stay: Sample Inn West"))
    }

    func testAHotelWithNoChargerSaysSoAndNamesTheMorningCharger() throws {
        let stay = try XCTUnwrap(try answer().fallbackStays.first)
        XCTAssertEqual(TripPlanLogic.fallbackLine(stay, metric: false), "No charger listed here. Nearest fast charger: Sample Fast Charge A, 2 mi away (250 kW).")
        XCTAssertEqual(TripPlanLogic.candidate(stay).l2Kw, 0, "no charging overnight")
        XCTAssertEqual(TripPlanLogic.osmCredit, "© OpenStreetMap contributors")
        XCTAssertEqual(try answer().attributionLicenseUrl, "https://opendatacommons.org/licenses/odbl/1-0/")
    }

    func testTheMorningChargerIsDayTwosFirstStop() throws {
        let found = try answer()
        let stay = try XCTUnwrap(found.fallbackStays.first)
        let req = TripPlanLogic.multidayRequest(
            vehicleID: nil, startingSoc: nil, packKwh: nil, startAt: Date(timeIntervalSince1970: 1_800_000_000),
            origin: (45.52, -122.68), destination: (38.58, -121.49), stay: TripPlanLogic.candidate(stay), morningCharger: stay.fastCharger)
        XCTAssertEqual(req.days[0].overnight?.l2Kw, 0)
        XCTAssertNil(req.days[0].vias)
        XCTAssertEqual(req.days[1].vias, [TripPoint(lat: 43.51, lon: -120.49, label: "Sample Fast Charge A", entityId: "e1")])
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        let text = String(decoding: try encoder.encode(req), as: UTF8.self)
        XCTAssertTrue(text.contains(#""entity_id":"e1""#), text)
        let plain = TripPlanLogic.multidayRequest(vehicleID: nil, startingSoc: nil, packKwh: nil, startAt: Date(), origin: (45.52, -122.68), destination: (38.58, -121.49),
                                                  stay: .init(name: "Sample Inn", lat: 45.9, lon: -121.0, l2Kw: 7))
        XCTAssertNil(plain.days[1].vias, "a stay with a charger starts the day as before")
    }

    func testWithNoListedStayInRangeTheModelOffersTheHotelsWithNoCharger() async throws {
        let src = FakeTrips()
        src.plan = try longPlan()
        src.stays = try answer()
        let m = TripPlanModel()
        await planned(src, m)
        await m.findNight(source: src)
        guard case .chooseFallback(let shown) = m.night else { return XCTFail("\(m.night)") }
        XCTAssertEqual(shown.fallbackStays.map(\.name), ["Sample Motel"])
        XCTAssertTrue(src.dayRequests.isEmpty, "nothing is planned until one is picked")
        let stay = try XCTUnwrap(shown.fallbackStays.first)
        src.days = try Fixture.decode(TripMultiday.self, "trip_plan_multiday")
        await m.planNight(source: src, at: TripPlanLogic.candidate(stay), morning: stay.fastCharger)
        XCTAssertEqual(src.dayRequests.first?.days[1].vias?.first?.entityId, "e1")
        guard case .planned = m.night else { return XCTFail("\(m.night)") }
    }
}
