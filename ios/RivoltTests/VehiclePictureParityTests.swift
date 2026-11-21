import XCTest
@testable import Rivolt

/// The Live picture's pick, one table for both platforms: `Parity/vehicle_picture_parity.json` is also read by the web's
/// `lib/vehicleImage.test.ts`, so a change to the rule on one side fails the other until it follows.
final class VehiclePictureParityTests: XCTestCase {
    private struct Table: Decodable {
        struct State: Decodable {
            var gear: String?
            var doorsClosed: Bool?
            var frunkClosed: Bool?
            var liftgateClosed: Bool?
            var tonneauClosed: Bool?
        }
        struct Case: Decodable {
            let name: String
            let state: State?
            let light: Bool
            let images: [VehicleImage]?
            let imageUrl: String?
            let failed: [String]?
            let expect: String
        }
        let images: [VehicleImage]
        let cases: [Case]
    }

    func testTheLivePickMatchesTheSharedTable() throws {
        let table = try Fixture.decode(Table.self, "vehicle_picture_parity")
        XCTAssertGreaterThan(table.cases.count, 10)
        for c in table.cases {
            let inputs = c.state.map {
                VehiclePicture.LiveInputs(gear: $0.gear, doorsClosed: $0.doorsClosed, frunkClosed: $0.frunkClosed,
                                          liftgateClosed: $0.liftgateClosed, tonneauClosed: $0.tonneauClosed)
            }
            let got = VehiclePicture.liveCandidates(c.images ?? table.images, imageURL: c.imageUrl, state: inputs, light: c.light,
                                                    failed: Set((c.failed ?? []).compactMap(URL.init(string:)))).first
            XCTAssertEqual(got?.absoluteString ?? "", c.expect, c.name)
        }
    }

    func testTheStateDecodesTheClosures() throws {
        let s = try Fixture.decode(VehicleState.self, "state_full")
        XCTAssertEqual(s.doorsClosed, false)
        XCTAssertEqual(s.frunkClosed, false)
    }

    /// The web's panel rule (LivePanel.tsx isDriving / isCharging): where the drive or charge panel takes the width there is no picture.
    func testTheLivePictureIsAbsentWhereTheWebPanelReplacesIt() {
        func shows(_ s: VehiclePicture.LiveInputs?, kind: NowSnapshot.State = .parked, streaming: Bool = true) -> Bool { VehiclePicture.showsOnLive(kind: kind, state: s, streaming: streaming) }
        let plugged = "chrgr_sts_connected_charging"
        XCTAssertFalse(shows(.init(gear: "D")), "drive")
        XCTAssertFalse(shows(.init(gear: "r")), "reverse")
        XCTAssertFalse(shows(.init(gear: "N")), "neutral")
        XCTAssertTrue(shows(.init(gear: "P")), "park")
        XCTAssertFalse(shows(.init(chargerState: "charging_active", chargerStatus: plugged)), "charging_active")
        XCTAssertFalse(shows(.init(chargerState: "charging_ready", chargerStatus: plugged)), "charging_ready")
        XCTAssertFalse(shows(.init(chargerState: "", chargerStatus: plugged, chargerPowerKw: 7)), "power with the cable in")
        XCTAssertTrue(shows(.init(chargerState: "charging_complete", chargerStatus: plugged, chargerPowerKw: 7)), "complete")
        XCTAssertTrue(shows(.init(chargerState: "charging_active", chargerStatus: "chrgr_sts_not_connected")), "the state sticks after an unplug")
        XCTAssertFalse(shows(.init(chargerState: "charging_active")), "an R2 reports charging_active and no plug status")
        XCTAssertTrue(shows(.init(chargerState: "charging_ready")), "ready without the cable in")
        XCTAssertFalse(shows(nil, kind: .charging), "the server says charging")
        XCTAssertFalse(shows(nil, kind: .driving), "the server says driving")
        XCTAssertTrue(shows(nil, kind: .driving, streaming: false), "stale, whatever the kind")
        XCTAssertTrue(shows(.init(gear: "P", chargerState: "charger_disconnected")), "parked")
        XCTAssertTrue(shows(nil), "no state yet")
        XCTAssertTrue(shows(.init(gear: "D"), streaming: false), "stale: the phone's panels are not shown, so the picture stays")
        XCTAssertTrue(shows(.init(chargerState: "charging_active", chargerStatus: plugged), streaming: false), "stale charging")
    }
}
