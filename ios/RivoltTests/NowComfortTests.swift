import XCTest
@testable import Rivolt

final class NowComfortTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func summary(comfort: NowComfort?, age: TimeInterval = 20, holdOffline: Bool = false) -> NowSummary {
        let input = NowSummary.Input(socPct: 70, rangeKm: 300, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_disconnected",
                                     chargerPowerKw: 0, at: t0, comfort: comfort)
        var s = NowSummary(input: input, now: nil, clock: t0.addingTimeInterval(age))
        s.holdOffline = holdOffline
        return s
    }

    func testWordsAreTheTrucksOwnAndNothingElse() throws {
        let c = try XCTUnwrap(NowComfort(cabinTempC: 20, preconditioning: "preconditioning", petMode: "on", cabinHold: "active"))
        XCTAssertEqual(c.parts().count, 4)
        XCTAssertEqual(Array(c.parts().dropFirst()), ["Preconditioning", "Pet mode on", "Climate hold"])
        XCTAssertTrue(try XCTUnwrap(c.parts().first).hasPrefix("Cabin "))
        XCTAssertEqual(try XCTUnwrap(NowComfort(cabinTempC: nil, preconditioning: "preconditioned", petMode: nil, cabinHold: nil)).text, "Preconditioned")
    }

    func testUnknownOffDisabledAndFaultyAreLeftOut() {
        for v in ["", "undefined", "unknown", "off", "disabled", "faulty", "not_preconditioning", "false", "0", "garbage"] {
            XCTAssertNil(NowComfort(cabinTempC: nil, preconditioning: v, petMode: v, cabinHold: v), v)
        }
        XCTAssertNil(NowComfort(cabinTempC: 0, preconditioning: nil, petMode: nil, cabinHold: nil), "0 is no reading")
        XCTAssertNil(NowComfort(cabinTempC: 120, preconditioning: nil, petMode: nil, cabinHold: nil))
        XCTAssertNil(NowComfort(cabinTempC: nil, preconditioning: nil, petMode: nil, cabinHold: nil))
    }

    func testPetModeIsPlainTextWithNoSafetyClaim() throws {
        let c = try XCTUnwrap(NowComfort(cabinTempC: nil, preconditioning: nil, petMode: "on", cabinHold: nil))
        XCTAssertEqual(c.text, "Pet mode on")
        XCTAssertEqual(c.spokenText, "Pet mode on")
        for banned in ["safe", "limit", "warning", "alert", "⚠"] { XCTAssertFalse(c.text.lowercased().contains(banned)) }
    }

    func testOnlyAFreshFrameShowsTheLine() throws {
        let c = NowComfort(cabinTempC: nil, preconditioning: nil, petMode: "on", cabinHold: nil)
        XCTAssertNotNil(summary(comfort: c, age: 20).liveComfort)
        XCTAssertNil(summary(comfort: c, age: 600).liveComfort, "stale")
        XCTAssertNil(summary(comfort: c, age: 3 * 3600).liveComfort, "offline")
        XCTAssertNil(summary(comfort: c, age: 3 * 3600, holdOffline: true).liveComfort, "offline held as stale is still not live")
        XCTAssertNil(summary(comfort: nil).liveComfort)
    }

    func testTheStateDecodesTheComfortFields() throws {
        let json = #"{"at":"2026-10-21T15:00:00Z","vehicle_id":"v","battery_level_pct":70,"distance_to_empty":300,"cabin_temp_c":21.5,"cabin_preconditioning_status":"preconditioned","pet_mode_status":"on","cabin_hold_status":"","latitude":1.0}"#
        let s = try JSON.decoder().decode(VehicleState.self, from: Data(json.utf8))
        XCTAssertEqual(s.cabinTempC, 21.5)
        let c = try XCTUnwrap(NowSummary.Input(s).comfort)
        XCTAssertTrue(c.petMode)
        XCTAssertFalse(c.climateHold)
        XCTAssertEqual(c.preconditioning, .done)
    }
}
