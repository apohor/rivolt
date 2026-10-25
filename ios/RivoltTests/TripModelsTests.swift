import XCTest
@testable import Rivolt

/// The trip planner's wire shapes against the server's own recorded responses (backend's fixtures, decoded strictly by Go): the
/// Go-cased plan, the multi-day wrapper with its partial failure, saved trips, and the app's own request bodies.
final class TripModelsTests: XCTestCase {
    func testThePlanReadsItsGoCasedKeysAndTheCosts() throws {
        let plan = try Fixture.decode(TripPlan.self, "trip_plan")
        let best = try XCTUnwrap(plan.best)
        XCTAssertEqual(plan.status, "SUCCESS")
        XCTAssertEqual(best.destinationReached, true)
        XCTAssertGreaterThan(best.totalDriveDurationSec ?? 0, 0)
        XCTAssertGreaterThan(best.totalDriveDistanceMeters ?? 0, 0)
        let stops = best.chargingStops
        XCTAssertFalse(stops.isEmpty, "the fixture has a fast-charging stop")
        XCTAssertEqual(stops.first?.name, "Example Fast Charge, Ellensburg")
        XCTAssertEqual(stops.first?.maxPowerKW, 200)
        XCTAssertEqual(stops.first?.chargeDurationSec, 1500)
        XCTAssertEqual(stops.first?.arrivalSoC, 31)
        XCTAssertEqual(stops.first?.departureSoC, 72)
        XCTAssertNotNil(stops.first?.arrivalTimeUTC?.date)
        let cost = try XCTUnwrap(plan.costs?.first)
        XCTAssertEqual(cost.currency, "USD")
        XCTAssertEqual(cost.dcfcSpendLow, 11.5)
        XCTAssertEqual(cost.dcfcSpendHigh, 14)
        XCTAssertEqual(cost.pricedStops, 1)
        XCTAssertEqual(cost.estimateBasis, "observed")
        XCTAssertEqual(cost.homeEquivalent, 9.8)
    }

    func testAnOriginHasNoArrivalTimeButStillReads() throws {
        let origin = try XCTUnwrap(try Fixture.decode(TripPlan.self, "trip_plan").best?.waypoints.first)
        XCTAssertEqual(origin.waypointType, "origin")
        XCTAssertNil(origin.arrivalTimeUTC?.date, "the server sends an empty string for an origin's arrival")
        XCTAssertNotNil(origin.departureTimeUTC?.date)
        XCTAssertFalse(origin.isCharger)
    }

    func testAPlanWithUnknownOrMissingFieldsStillReads() throws {
        let odd = try JSON.decoder().decode(TripPlan.self, from: Data(#"{"Status":"OK","Routes":[{"DestinationReached":"yes","Waypoints":[{"Name":5}],"Brand":"new"}],"Surprise":1}"#.utf8))
        XCTAssertEqual(odd.routes.count, 1)
        XCTAssertNil(odd.routes[0].destinationReached, "a field of the wrong type is nil, not a failed screen")
        XCTAssertNil(odd.routes[0].waypoints[0].name)
        let none = try JSON.decoder().decode(TripPlan.self, from: Data("{}".utf8))
        XCTAssertNil(none.best)
        XCTAssertNil(none.costs)
    }

    func testTheMultiDayPlanAndItsPartialFailure() throws {
        let full = try Fixture.decode(TripMultiday.self, "trip_plan_multiday")
        XCTAssertFalse(full.failed)
        XCTAssertFalse(full.days.isEmpty)
        XCTAssertNotNil(full.total?.distanceMeters)
        XCTAssertEqual(full.tz, "America/Los_Angeles")
        let partial = try Fixture.decode(TripMultiday.self, "trip_plan_multiday_partial")
        XCTAssertEqual(partial.failedDay, 2)
        XCTAssertEqual(partial.errorClass, "transient")
        XCTAssertEqual(partial.error, "Day 2 could not be planned right now. Try again in a minute.")
        let day1 = try XCTUnwrap(partial.days.first)
        XCTAssertEqual(day1.index, 1)
        XCTAssertEqual(day1.start?.label, "Example Origin")
        XCTAssertEqual(day1.overnight?.name, "Example Hotel with L2")
        XCTAssertEqual(day1.overnight?.l2Kw, 7.2)
        XCTAssertEqual(day1.departureSoc, 88)
        XCTAssertNotNil(day1.plan.best)
    }

    func testSavedTripsKeepTheirBlobsAsTheyCame() throws {
        let list = try Fixture.decode([SavedTrip].self, "trip_saved_list")
        XCTAssertFalse(list.isEmpty)
        let one = try Fixture.decode(SavedTrip.self, "trip_saved_one")
        XCTAssertEqual(one.id, list.first?.id)
        XCTAssertNotNil(one.createdAt)
        guard case .object(let inputs)? = one.inputs else { return XCTFail("inputs is an object") }
        XCTAssertEqual(inputs["pack_kwh"], .number(135))
        // The plan inside reads as a plan; an unreadable blob is nil, never a crash.
        XCTAssertNotNil(one.plan?.decoded(TripPlan.self)?.best)
        XCTAssertNil(JSONValue.string("nope").decoded(TripPlan.self))
        // And it writes back byte-for-byte equal in meaning.
        let again = try JSON.decoder().decode(JSONValue.self, from: try JSONEncoder().encode(one.inputs))
        XCTAssertEqual(again, one.inputs)
    }

    func testTheRequestBodiesAreSnakeCaseAndOnlyWhatIsSet() throws {
        let body = TripPlanRequest(vehicleId: "veh-1", startingSoc: 88, departAt: "2027-01-12T16:00:00Z",
                                   waypoints: [TripPlanWaypoint(latitude: 47.6, longitude: -122.3, waypointType: "origin"), TripPlanWaypoint(latitude: 47.7, longitude: -117.4, waypointType: "destination")],
                                   packKwh: 135)
        let enc = JSONEncoder()
        enc.keyEncodingStrategy = .convertToSnakeCase
        let obj = try XCTUnwrap(try JSONSerialization.jsonObject(with: try enc.encode(body)) as? [String: Any])
        XCTAssertEqual(Set(obj.keys), ["vehicle_id", "starting_soc", "origin_bearing", "depart_at", "waypoints", "pack_kwh"])
        let wp = try XCTUnwrap((obj["waypoints"] as? [[String: Any]])?.first)
        XCTAssertEqual(Set(wp.keys), ["latitude", "longitude", "waypoint_type"])
        let saved = SavedTripBody(name: "Seattle to Spokane", inputs: .object(["pack_kwh": .number(135)]), plan: nil)
        let s = try XCTUnwrap(try JSONSerialization.jsonObject(with: try enc.encode(saved)) as? [String: Any])
        XCTAssertEqual(Set(s.keys), ["name", "inputs"])
    }
}
