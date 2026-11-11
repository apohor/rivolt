import XCTest
@testable import Rivolt

/// Single ownership: a 503 with Retry-After means the server that holds the truck's Rivian connection did not answer.
/// It stays an `APIError.server(503, …)`, so every screen that already handled a 503 keeps doing what it did; only the
/// planner says "Reconnecting to Rivian" (see TripPlanTests), and Home keeps the last state of the same truck.
@MainActor
final class ReconnectingTests: XCTestCase {
    private let reconnecting = APIError.server(status: 503, message: "the vehicle's owner did not answer; try again", retryAfter: 5)

    func testRetryAfterIsReadInWholeSecondsOnly() {
        XCTAssertEqual(APIError.retryAfterSeconds("5"), 5)
        XCTAssertEqual(APIError.retryAfterSeconds(" 5 "), 5)
        XCTAssertNil(APIError.retryAfterSeconds(nil))
        XCTAssertNil(APIError.retryAfterSeconds("Wed, 11 Nov 2026 07:28:00 GMT"))
        XCTAssertNil(APIError.retryAfterSeconds("-1"))
    }

    func testEveryExisting503HandlingIsUnchanged() {
        let plain = APIError.server(status: 503, message: "the vehicle's owner did not answer; try again")
        XCTAssertEqual(VehicleDiscoveryState.from(nil, error: reconnecting), .looking, "discovery's 503 + Retry-After is still looking")
        XCTAssertEqual(UserFacingError.words(reconnecting), UserFacingError.words(plain))
        XCTAssertEqual(ActivityModel.problem(for: reconnecting), "Rivolt isn't answering right now. Pull to try again.")
        XCTAssertEqual(ChargingRatesModel.saveError(reconnecting), .serverError)
        XCTAssertEqual(NowReads.describe(reconnecting), "HTTP 503")
        XCTAssertEqual(ConnectionDiagnostics.Outcome(reconnecting), .http(503))
        XCTAssertEqual(reconnecting.errorDescription, plain.errorDescription, "never the header in the words")
    }

    func testHomeKeepsTheLastStateOnlyForTheSameTruck() throws {
        let last = try Fixture.decode(VehicleState.self, "state_full")
        XCTAssertEqual(NowReads.stateToShow(fresh: nil, last: last, lastID: "truck-a", id: "truck-a"), last, "a failed read keeps this truck's values")
        XCTAssertNil(NowReads.stateToShow(fresh: nil, last: last, lastID: "truck-a", id: "truck-b"), "another truck never shows the old truck's state")
        XCTAssertNil(NowReads.stateToShow(fresh: nil, last: last, lastID: nil, id: "truck-a"))
        let fresh = try Fixture.decode(VehicleState.self, "state")
        XCTAssertEqual(NowReads.stateToShow(fresh: fresh, last: last, lastID: "truck-a", id: "truck-b"), fresh)
    }
}
