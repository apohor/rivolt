import XCTest
@testable import Rivolt

private final class FakeSaved: SavedTripSource, @unchecked Sendable {
    var listed: [SavedTrip] = []
    var failure: Error?
    private(set) var created: [SavedTripBody] = []
    private(set) var updated: [(id: String, body: SavedTripBody)] = []

    func savedTrips() async throws -> [SavedTrip] {
        if let failure { throw failure }
        return listed
    }
    func saveTrip(_ body: SavedTripBody) async throws -> SavedTrip {
        created.append(body)
        if let failure { throw failure }
        return SavedTrip(id: "new-\(created.count)", name: body.name, inputs: body.inputs, plan: body.plan)
    }
    func updateSavedTrip(id: String, _ body: SavedTripBody) async throws -> SavedTrip {
        updated.append((id, body))
        if let failure { throw failure }
        return SavedTrip(id: id, name: body.name, inputs: body.inputs, plan: body.plan)
    }
}

@MainActor
final class SavedTripTests: XCTestCase {
    private let from = (lat: 47.62, lon: -122.35, label: "Seattle, Washington")
    private let to = (lat: 47.66, lon: -117.43, label: "Spokane, Washington")

    func testANameIsTheFirstPartOfEachPlace() {
        XCTAssertEqual(SavedTripLogic.suggestedName(origin: "Seattle, Washington, United States", destination: "Spokane, Washington"), "Seattle to Spokane")
        XCTAssertEqual(SavedTripLogic.suggestedName(origin: nil, destination: "Spokane, Washington"), "To Spokane")
        XCTAssertLessThanOrEqual(SavedTripLogic.suggestedName(origin: String(repeating: "A", count: 90), destination: "B").count, 60)
    }

    func testTheInputsAreTheWebsShapeWithNoTruckChargeAndNoNightForOneDay() throws {
        let v = SavedTripLogic.inputs(origin: from, destination: to, stay: nil, startingSoc: nil, departAt: nil)
        guard case .object(let o) = v else { return XCTFail() }
        XCTAssertEqual(o["extra_stops"], .array([]))
        XCTAssertEqual(o["starting_soc"], .string(""), "the truck's own charge is read live on a re-plan")
        XCTAssertEqual(o["target_soc"], .string("20"))
        XCTAssertEqual(o["departure_at"], .string(""))
        XCTAssertNil(o["overnight_flags"], "a one-day save stays as the web's one-day saves")
        guard case .object(let origin)? = o["origin"] else { return XCTFail() }
        XCTAssertEqual(origin["lat"], .number(47.62))
        XCTAssertEqual(origin["label"], .string("Seattle, Washington"))
    }

    func testATwoDayTripFlagsTheStayAsANight() throws {
        let stay = OvernightCandidates.Candidate(name: "Sample Inn", lat: 45.52, lon: -122.68, l2Kw: 7)
        let when = ISO8601DateFormatter().date(from: "2026-10-30T15:43:00Z")
        let v = SavedTripLogic.inputs(origin: from, destination: to, stay: stay, startingSoc: 55, departAt: when, timeZone: TimeZone(identifier: "America/Los_Angeles")!)
        guard case .object(let o) = v else { return XCTFail() }
        XCTAssertEqual(o["overnight_flags"], .array([.bool(true)]))
        XCTAssertEqual(o["starting_soc"], .string("55"), "a charge the person typed is kept")
        XCTAssertEqual(o["departure_at"], .string("2026-10-30T08:43"))
        guard case .array(let stops)? = o["extra_stops"], stops.count == 1 else { return XCTFail() }
        guard case .array(let settings)? = o["overnight_settings"], case .object(let s)? = settings.first else { return XCTFail() }
        XCTAssertEqual(s["until"], .string("08:00"))
        XCTAssertEqual(s["l2_kw"], .number(7))
    }

    func testASavedSingleTripOpensAsItWasSaved() throws {
        let one = try Fixture.decode(SavedTrip.self, "trip_saved_one")
        let r = try XCTUnwrap(SavedTripLogic.restore(one))
        XCTAssertEqual(r.origin.name, "Example Origin")
        XCTAssertEqual(r.destination.longitude, -117.426)
        XCTAssertNil(r.multiday)
        XCTAssertNotNil(r.plan.best)
    }

    func testATwoDayTripRoundTripsThroughTheSavedShape() throws {
        let trip = try Fixture.decode(TripMultiday.self, "trip_plan_multiday")
        let blob = try XCTUnwrap(SavedTripLogic.planBlob(plan: nil, multiday: trip))
        guard case .object(let o) = blob else { return XCTFail() }
        XCTAssertEqual(o["kind"], .string("multiday"))
        guard case .array(let days)? = o["days"] else { return XCTFail() }
        XCTAssertEqual(days.count, trip.days.count)
        let stay = OvernightCandidates.Candidate(name: "Sample Inn", lat: 45.52, lon: -122.68, l2Kw: 7)
        let saved = SavedTrip(id: "x", name: "Two days", inputs: SavedTripLogic.inputs(origin: from, destination: to, stay: stay, startingSoc: nil, departAt: nil), plan: blob)
        let r = try XCTUnwrap(SavedTripLogic.restore(saved))
        XCTAssertEqual(r.multiday?.days.count, trip.days.count)
        XCTAssertNotNil(r.stay)
    }

    func testATripThatDoesNotReadIsNilAndNotACrash() {
        XCTAssertNil(SavedTripLogic.restore(SavedTrip(id: "a", name: "No plan", inputs: .object([:]), plan: nil)))
        XCTAssertNil(SavedTripLogic.restore(SavedTrip(id: "b", name: "Odd", inputs: .string("x"), plan: .string("y"))))
    }

    func testEveryFailureIsOurOwnWords() {
        XCTAssertTrue(SavedTripLogic.failure(APIError.server(status: 409, message: "trip name already in use")).hasPrefix("You already have a saved trip"))
        XCTAssertEqual(SavedTripLogic.failure(APIError.server(status: 413, message: "x")), "This trip is too big to save.")
        XCTAssertEqual(SavedTripLogic.failure(APIError.server(status: 422, message: "you can keep up to 100")), "You have 100 saved trips. Delete one on the web to save another.")
    }

    func testSavingCreatesThenUpdatesTheOpenedTripAndKeepsTheList() async throws {
        let src = FakeSaved()
        let m = SavedTripsModel()
        await m.save(source: src, name: "  Seattle to Spokane ", inputs: .object([:]), plan: nil, overwrite: false)
        XCTAssertEqual(src.created.first?.name, "Seattle to Spokane", "trimmed")
        XCTAssertEqual(m.saveState, .saved("Seattle to Spokane"))
        XCTAssertEqual(m.active?.id, "new-1")
        XCTAssertEqual(m.trips.count, 1)
        await m.save(source: src, name: "Seattle to Spokane", inputs: .object(["a": .number(1)]), plan: nil, overwrite: true)
        XCTAssertEqual(src.updated.first?.id, "new-1")
        XCTAssertEqual(src.created.count, 1, "an update is not a second trip")
        XCTAssertEqual(m.trips.count, 1)
    }

    func testANameTakenOrABlankNameIsSaidOrNotSent() async {
        let src = FakeSaved()
        let m = SavedTripsModel()
        await m.save(source: src, name: "   ", inputs: .object([:]), plan: nil, overwrite: false)
        XCTAssertTrue(src.created.isEmpty)
        src.failure = APIError.server(status: 409, message: "trip name already in use")
        await m.save(source: src, name: "Taken", inputs: .object([:]), plan: nil, overwrite: false)
        guard case .failed(let words) = m.saveState else { return XCTFail() }
        XCTAssertFalse(words.contains("trip name already in use"), "never the server's text")
        XCTAssertNil(m.active)
    }

    func testTheListLoadsAndAFailureIsSaid() async throws {
        let src = FakeSaved()
        src.listed = [try Fixture.decode(SavedTrip.self, "trip_saved_one")]
        let m = SavedTripsModel()
        await m.load(source: src)
        XCTAssertEqual(m.trips.count, 1)
        XCTAssertEqual(m.phase, .loaded)
        src.failure = APIError.server(status: 500, message: "boom")
        let m2 = SavedTripsModel()
        await m2.load(source: src)
        XCTAssertEqual(m2.phase, .failed("The planner isn't answering right now. Try again in a minute."))
    }
}
