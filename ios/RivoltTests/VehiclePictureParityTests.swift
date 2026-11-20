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

    /// Driving or charging, the Live screen has no picture and the panel takes the width; every other state keeps it.
    func testTheLivePictureIsAbsentWhileDrivingOrCharging() {
        XCTAssertFalse(VehiclePicture.showsOnLive(kind: .driving, streaming: true))
        XCTAssertFalse(VehiclePicture.showsOnLive(kind: .charging, streaming: true))
        XCTAssertTrue(VehiclePicture.showsOnLive(kind: .parked, streaming: true))
        XCTAssertTrue(VehiclePicture.showsOnLive(kind: .driving, streaming: false), "a stale truck keeps its picture")
    }
}
