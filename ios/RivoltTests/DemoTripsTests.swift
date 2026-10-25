import XCTest
@testable import Rivolt

private final class FakeDemoTrips: DemoTripsSource, @unchecked Sendable {
    var failure: Error?
    private(set) var plansRead: [String] = []

    func demoTrips() async throws -> DemoTrips {
        if let failure { throw failure }
        return try Fixture.decode(DemoTrips.self, "demo_trips")
    }
    func demoTripPlan(id: String) async throws -> JSONValue {
        plansRead.append(id)
        if let failure { throw failure }
        switch id {
        case "mammoth": return try Fixture.decode(JSONValue.self, "demo_trip_plan_single")
        case "919ca13a-123f-5423-994d-12d7ccd31319": return try Fixture.decode(JSONValue.self, "demo_trip_plan_two_days")
        default: throw APIError.server(status: 404, message: "no such sample")
        }
    }
}

@MainActor
final class DemoTripsTests: XCTestCase {
    private func presets() throws -> [DemoTripPreset] { try Fixture.decode(DemoTrips.self, "demo_trips").trips }

    func testTheSampleListReadsAndEachCardHasItsFigures() throws {
        let list = try presets()
        XCTAssertGreaterThanOrEqual(list.count, 5)
        let mammoth = try XCTUnwrap(list.first { $0.id == "mammoth" })
        XCTAssertEqual(mammoth.name, "LA to Mammoth Lakes")
        XCTAssertNotNil(mammoth.routePolyline)
        XCTAssertEqual(DemoTripLogic.summary(mammoth.summary, metric: false), "315 mi · 5 h 38 min · 1 charging stop · arrives with about 16%")
        XCTAssertEqual(DemoTripLogic.summary(nil, metric: false), "")
    }

    func testASampleTripOpensAsAOneDayPlanWithItsRouteShapeAndItsStop() async throws {
        let src = FakeDemoTrips()
        let demo = DemoTripsModel()
        let plan = TripPlanModel()
        let preset = try XCTUnwrap(try presets().first { $0.id == "mammoth" })
        await demo.open(preset, source: src, on: plan)
        XCTAssertEqual(src.plansRead, ["mammoth"])
        XCTAssertEqual(demo.openedID, "mammoth")
        let route = try XCTUnwrap(plan.plan?.best)
        XCTAssertEqual(route.chargingStops.count, 1)
        XCTAssertNotNil(route.routePolyline, "the map follows the roads with no router, which the demo can't ask")
        XCTAssertNotNil(plan.destination)
        XCTAssertNil(demo.message)
    }

    func testTheTwoDayRunOpensAsTwoDaysWithItsNight() async throws {
        let src = FakeDemoTrips()
        let demo = DemoTripsModel()
        let plan = TripPlanModel()
        let preset = try XCTUnwrap(try presets().first { $0.id == "919ca13a-123f-5423-994d-12d7ccd31319" })
        await demo.open(preset, source: src, on: plan)
        guard case .planned(let trip) = plan.night else { return XCTFail("\(plan.night)") }
        XCTAssertEqual(trip.days.count, 2)
        XCTAssertNotNil(plan.chosenStay)
    }

    func testAFailureIsSaidInOurWordsAndLoadingTheListWorks() async throws {
        let src = FakeDemoTrips()
        let demo = DemoTripsModel()
        await demo.load(source: src)
        guard case .loaded(let list) = demo.phase else { return XCTFail("\(demo.phase)") }
        XCTAssertGreaterThanOrEqual(list.count, 5)
        let plan = TripPlanModel()
        src.failure = APIError.server(status: 500, message: "boom")
        await demo.open(list[0], source: src, on: plan)
        XCTAssertEqual(demo.message, "Couldn't open that sample trip. Try again in a minute.")
        XCTAssertNil(plan.plan)
        let broken = DemoTripsModel()
        await broken.load(source: src)
        XCTAssertEqual(broken.phase, .failed("Couldn't load the sample trips. Try again in a minute."))
    }
}
