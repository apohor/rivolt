import XCTest
@testable import Rivolt

final class ModelDecodingTests: XCTestCase {
    func testNowSnapshotDecodesEveryField() throws {
        let n = try Fixture.decode(NowSnapshot.self, "now_v1")
        XCTAssertEqual(n.version, 1)
        XCTAssertEqual(n.connection, .live)
        XCTAssertEqual(n.state, .charging)
        XCTAssertEqual(n.asleep, false)
        XCTAssertEqual(n.socAtStateStart, 41.5)
        XCTAssertEqual(n.tirePressureMinBar, 2.9)
        XCTAssertEqual(n.charge?.peakKw, 11.2)
        XCTAssertEqual(n.ambient?.tempC, 12.5)
        XCTAssertNotNil(n.at)
        XCTAssertNotNil(n.vehicleStateFrameAt)
    }

    /// `.convertFromSnakeCase` capitalizes "6h" to "6H"; NowSnapshot's
    /// CodingKeys depend on that.
    func testFailedSleepsKeyConversion() throws {
        let n = try JSON.decoder().decode(NowSnapshot.self, from: Data(#"{"version":1,"at":null,"location_fix_at":null,"connection":"live","state":"parked","state_since":null,"soc_at_state_start":null,"tire_pressure_min_bar":null,"failed_sleeps_6h":3,"vehicle_state_frame_at":null,"charge":null}"#.utf8))
        XCTAssertEqual(n.failedSleeps6h, 3)
    }

    func testNowSnapshotWithNullsAndMissingOptionalFields() throws {
        let n = try Fixture.decode(NowSnapshot.self, "now_v1_minimal")
        XCTAssertEqual(n.connection, .paused)
        XCTAssertEqual(n.state, .unknown)
        XCTAssertNil(n.at)
        XCTAssertNil(n.asleep)
        XCTAssertNil(n.charge)
        XCTAssertNil(n.ambient)
    }

    func testVehicleStateDecodes() throws {
        let s = try Fixture.decode(VehicleState.self, "state")
        XCTAssertEqual(s.vehicleId, "01-242521064")
        XCTAssertEqual(s.batteryLevelPct, 63.4)
        XCTAssertEqual(s.distanceToEmpty, 312.7)
        XCTAssertEqual(s.chargerState, "charging_active")
        XCTAssertEqual(s.chargeTargetPct, 80)
        XCTAssertNotNil(s.at)
    }

    func testVehiclesDecode() throws {
        let vs = try Fixture.decode([Vehicle].self, "vehicles")
        XCTAssertEqual(vs.map(\.id), ["01-242521064", "01-242521065"])
        XCTAssertNil(vs[1].modelYear)
    }

    /// Go sends up to nine fractional digits; the scaffold's .iso8601 strategy
    /// rejected any fraction at all.
    func testDatesWithAndWithoutFractions() throws {
        let whole = try XCTUnwrap(JSON.parseDate("2026-10-21T14:03:12Z"))
        let nanos = try XCTUnwrap(JSON.parseDate("2026-10-21T14:03:12.123456789Z"))
        XCTAssertEqual(nanos.timeIntervalSince(whole), 0.123, accuracy: 0.001)
        XCTAssertNotNil(JSON.parseDate("2026-10-21T14:03:12.5Z"))
        XCTAssertNotNil(JSON.parseDate("2026-10-21T09:03:12.25-05:00"))
        XCTAssertNil(JSON.parseDate("yesterday"))
    }
}
