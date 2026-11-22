import XCTest
@testable import Rivolt

final class ChargePulseTests: XCTestCase {
    func testPowerFlowingIsCharging() {
        XCTAssertEqual(ChargePulse.state(on: true, kw: 11, plugged: true), .charging)
        XCTAssertEqual(ChargePulse.state(on: true, kw: 0.1, plugged: false), .charging)
    }

    func testPluggedWithoutPowerIsPlugged() {
        XCTAssertEqual(ChargePulse.state(on: true, kw: 0, plugged: true), .plugged)
        XCTAssertEqual(ChargePulse.state(on: true, kw: nil, plugged: true), .plugged)
    }

    func testUnpluggedWithoutPowerIsNone() {
        XCTAssertEqual(ChargePulse.state(on: true, kw: 0, plugged: false), .none)
        XCTAssertEqual(ChargePulse.state(on: true, kw: nil, plugged: false), .none)
    }

    func testKeyOffIsAlwaysNone() {
        for kw in [nil, 0, 11] as [Double?] {
            for plugged in [true, false] { XCTAssertEqual(ChargePulse.state(on: false, kw: kw, plugged: plugged), .none) }
        }
        XCTAssertEqual(ChargePulse.state(on: false, kind: .charging, powerKw: 11, offline: false), .none)
    }

    func testSummaryKindsOnly() {
        XCTAssertEqual(ChargePulse.state(on: true, kind: .charging, powerKw: 11, offline: false), .charging)
        XCTAssertEqual(ChargePulse.state(on: true, kind: .plugged, powerKw: 0, offline: false), .plugged)
        XCTAssertEqual(ChargePulse.state(on: true, kind: .parked, powerKw: nil, offline: false), .none)
        XCTAssertEqual(ChargePulse.state(on: true, kind: .driving, powerKw: 5, offline: false), .none)
    }

    func testOfflineTruckShowsNoCue() {
        XCTAssertEqual(ChargePulse.state(on: true, kind: .charging, powerKw: 11, offline: true), .none)
        XCTAssertEqual(ChargePulse.state(on: true, kind: .plugged, powerKw: 0, offline: true), .none)
    }
}
