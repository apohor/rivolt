import XCTest
@testable import Rivolt

final class NowSummaryTests: XCTestCase {
    private func fixtures() throws -> (VehicleState, NowSnapshot) {
        (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
    }

    func testChargingTruckJustSeen() throws {
        let (st, now) = try fixtures()
        let clock = try XCTUnwrap(now.at).addingTimeInterval(60)
        let s = NowSummary(state: st, now: now, clock: clock)
        XCTAssertEqual(s.stateLabel, "Charging")
        XCTAssertEqual(s.freshness, .fresh)
        XCTAssertEqual(s.chargePowerKw, 11.1)
        XCTAssertEqual(s.chargeTargetPct, 80)
        XCTAssertEqual(try XCTUnwrap(s.chargeAddedPct), 21.9, accuracy: 0.001)
        XCTAssertEqual(s.chargePeakKw, 11.2)
        XCTAssertEqual(s.outsideTempC, 12.5)
    }

    func testStaleAfterTheStatesCadence() throws {
        let (st, now) = try fixtures()
        let s = NowSummary(state: st, now: now, clock: try XCTUnwrap(now.at).addingTimeInterval(11 * 60))
        XCTAssertEqual(s.freshness, .stale)
    }

    func testReauthAndPausedWinOverAge() throws {
        let (st, _) = try fixtures()
        let paused = try Fixture.decode(NowSnapshot.self, "now_v1_minimal")
        XCTAssertEqual(NowSummary(state: st, now: paused, clock: .now).freshness, .paused)
        XCTAssertEqual(NowSummary(state: nil, now: nil, clock: .now).freshness, .unknown)
    }

    func testWithoutNowTheStateIsInferredFromState() throws {
        let (st, _) = try fixtures()
        let s = NowSummary(state: st, now: nil, clock: .now)
        XCTAssertEqual(s.stateLabel, "Charging")
        XCTAssertNil(s.chargeAddedPct)
    }

    func testLabels() {
        XCTAssertEqual(NowSummary.label(.plugged, asleep: true), "Plugged in, asleep")
        XCTAssertEqual(NowSummary.label(.asleep, asleep: true), "Asleep")
        XCTAssertEqual(NowSummary.label(.unknown, asleep: false), "Status unknown")
    }
}
