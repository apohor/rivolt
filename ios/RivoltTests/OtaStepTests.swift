import XCTest
@testable import Rivolt

/// The update step the server derives (`ota_step`, `ota_install_percent` on `GET /api/state`), and what the Truck tab and Now say about it (the web's
/// `otaStep` cases).
final class OtaStepTests: XCTestCase {
    private func step(_ step: String?, _ progress: Int? = nil, version: String? = nil) -> OtaStep? {
        OtaStep(step: step, progress: progress, version: version)
    }

    func testTheTenKnownStepsAreNamedInPlainWords() {
        let words = ["ready_to_download": "Ready to download", "downloading": "Downloading", "preparing": "Preparing", "ready_to_install": "Ready to install",
                     "scheduled_to_install": "Install scheduled", "install_countdown": "Install about to start", "awaiting_install": "Waiting to install",
                     "installing": "Installing", "install_success": "Installed", "connection_lost": "Update connection lost"]
        for (key, word) in words { XCTAssertEqual(step(key)?.word, word, key) }
    }

    func testUnknownAnUnrecognisedWordOrNoStepShowNothing() {
        for key in ["unknown", "download_failed", "Installing", ""] { XCTAssertNil(step(key), key) }
        XCTAssertNil(step(nil))
        XCTAssertNil(step(nil, 50))
        XCTAssertNil(OtaStep(nil as VehicleState?))
    }

    func testANumberOnlyWhileInstallingAndZeroIsAStart() {
        XCTAssertEqual(step("installing", 37)?.line, "Installing 37%")
        XCTAssertEqual(step("installing", 0)?.pct, 0)
        XCTAssertNil(step("installing")?.pct)
        XCTAssertNil(step("downloading", 40)?.pct)
        XCTAssertEqual(step("installing", 140)?.pct, 100)
    }

    func testItIsNeverInferred() {
        XCTAssertEqual(step("installing", 100)?.word, "Installing")
        XCTAssertEqual(step("install_success")?.word, "Installed")
    }

    func testThePendingVersionComesFirst() {
        XCTAssertEqual(step("downloading", version: "2026.38.0")?.line, "2026.38.0 · Downloading")
        XCTAssertEqual(step("downloading", version: "0.0.0")?.line, "Downloading")
        XCTAssertEqual(step("downloading", version: "")?.line, "Downloading")
    }

    func testTheLiveStateCarriesTheStepAndAnOlderServerCarriesNone() throws {
        let installing = try Fixture.decode(VehicleState.self, "state_ota_installing")
        XCTAssertEqual(OtaStep(installing)?.key, "installing")
        XCTAssertNotNil(OtaStep(installing)?.pct)
        let older = #"{"at":"2026-10-21T15:00:00Z","vehicle_id":"v","battery_level_pct":70,"distance_to_empty":300}"#
        XCTAssertNil(OtaStep(try JSON.decoder().decode(VehicleState.self, from: Data(older.utf8))), "no ota_step: nothing to show, not an error")
    }
}
