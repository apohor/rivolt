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

    func testCabinIsItsOwnRowInWholeDegreesAndTheLineHasTheRest() throws {
        let c = try XCTUnwrap(NowComfort(cabinTempC: 28.6, preconditioning: "preconditioning", petMode: "on", cabinHold: nil))
        let cabin = try XCTUnwrap(c.cabinValue)
        XCTAssertFalse(cabin.contains("."), "whole degrees: \(cabin)")
        XCTAssertTrue(cabin.contains("29") || cabin.contains("83"), cabin)
        XCTAssertEqual(c.lineText, "Preconditioning · Pet mode on")
        XCTAssertEqual(try XCTUnwrap(NowComfort(cabinTempC: 20, preconditioning: nil, petMode: nil, cabinHold: nil)).lineText, "", "nothing else to say, no line")
        XCTAssertNil(NowComfort(cabinTempC: nil, preconditioning: nil, petMode: "on", cabinHold: nil)?.cabinValue, "no reading, no row")
        XCTAssertFalse(NowComfort.wholeDegrees(celsius: 27.4).contains("."), "Outside is whole degrees too")
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

    // MARK: Software update

    private func update(_ status: String?, progress: Double? = 40, available: String? = "2026.38.0") -> NowUpdate? {
        NowUpdate(status: status, progress: progress, current: "2026.34.1", available: available)
    }

    func testTheUpdateReadsAFixedListOfTheTrucksWordsAndNothingElse() {
        XCTAssertEqual(update("Downloading")?.phase, .downloading)
        XCTAssertEqual(update("ready_to_install")?.phase, .ready)
        XCTAssertEqual(update("Ready To Install")?.phase, .ready)
        XCTAssertEqual(update("installing")?.phase, .installing)
        for v in ["Idle", "", "unknown", "undefined", "awaiting_reboot", "failed"] { XCTAssertNil(update(v), v) }
        XCTAssertNil(update(nil))
        XCTAssertNil(update("downloading", available: "0.0.0")?.version, "0.0.0 is no version")
    }

    func testNoNumberUntilTheProgressScaleIsConfirmed() {
        XCTAssertNil(NowUpdate.progressMax, "confirm the scale from real frames before showing a percent")
        XCTAssertNil(update("downloading", progress: 40)?.pct)
        XCTAssertEqual(update("downloading")?.line(), "2026.38.0 · Downloading")
        XCTAssertEqual(update("ready_to_install")?.line(), "2026.38.0 · Ready to install")
        XCTAssertEqual(update("installing")?.line(onNow: true), "Installing software 2026.38.0")
    }

    func testTheUpdateWordingGivesNoInstructionAndNoEstimate() throws {
        for v in ["downloading", "ready_to_install", "installing"] {
            let u = try XCTUnwrap(update(v))
            for l in [u.line(), u.line(onNow: true)] {
                XCTAssertNil(l.range(of: #"install now|minutes|about|safe|don't|do not"#, options: [.regularExpression, .caseInsensitive]), l)
            }
        }
    }

    func testOnlyAnInstallOnALiveFrameReachesNow() {
        func s(_ status: String, age: TimeInterval = 20) -> NowSummary {
            let input = NowSummary.Input(socPct: 70, rangeKm: 300, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_disconnected",
                                         chargerPowerKw: 0, at: t0, update: update(status))
            return NowSummary(input: input, now: nil, clock: t0.addingTimeInterval(age))
        }
        XCTAssertNotNil(s("installing").liveInstall)
        XCTAssertNil(s("downloading").liveInstall, "only an install shows on Now")
        XCTAssertNil(s("ready_to_install").liveInstall)
        XCTAssertNil(s("installing", age: 600).liveInstall, "stale")
        XCTAssertNil(s("installing", age: 3 * 3600).liveInstall, "offline")
    }
}
