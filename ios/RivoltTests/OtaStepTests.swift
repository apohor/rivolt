import XCTest
@testable import Rivolt

/// The update step the server reports, and what the Truck tab says about it (the web's `otaStep` cases).
final class OtaStepTests: XCTestCase {
    private func ota(_ fields: String) throws -> Vehicle.OTA {
        let json = #"{"current":{"version":"2026.30.0"},\#(fields)"fetched_at":"2026-10-17T10:00:00Z"}"#
        return try JSON.decoder().decode(Vehicle.OTA.self, from: Data(json.utf8))
    }

    func testTheTenKnownStepsAreNamedInPlainWords() throws {
        let words = ["ready_to_download": "Ready to download", "downloading": "Downloading", "preparing": "Preparing", "ready_to_install": "Ready to install",
                     "scheduled_to_install": "Install scheduled", "install_countdown": "Install about to start", "awaiting_install": "Waiting to install",
                     "installing": "Installing", "install_success": "Installed", "connection_lost": "Update connection lost"]
        for (step, word) in words { XCTAssertEqual(OtaStep(try ota(#""step":"\#(step)","#))?.word, word, step) }
    }

    func testUnknownAnUnrecognisedWordOrNoStepShowNothing() throws {
        for step in ["unknown", "download_failed", "Installing", ""] { XCTAssertNil(OtaStep(try ota(#""step":"\#(step)","#)), step) }
        XCTAssertNil(OtaStep(try ota("")))
        XCTAssertNil(OtaStep(try ota(#""install_progress":50,"#)))
        XCTAssertNil(OtaStep(nil))
    }

    func testANumberOnlyWhileInstallingAndZeroIsAStart() throws {
        XCTAssertEqual(OtaStep(try ota(#""step":"installing","install_progress":37,"#))?.line, "Installing 37%")
        XCTAssertEqual(OtaStep(try ota(#""step":"installing","install_progress":0,"#))?.pct, 0)
        XCTAssertNil(OtaStep(try ota(#""step":"installing","#))?.pct)
        XCTAssertNil(OtaStep(try ota(#""step":"downloading","install_progress":40,"#))?.pct)
        XCTAssertEqual(OtaStep(try ota(#""step":"installing","install_progress":140,"#))?.pct, 100)
    }

    func testItIsNeverInferred() throws {
        XCTAssertEqual(OtaStep(try ota(#""step":"installing","install_progress":100,"#))?.word, "Installing")
        XCTAssertEqual(OtaStep(try ota(#""step":"install_success","#))?.word, "Installed")
    }

    func testThePendingVersionComesFirst() throws {
        let json = #"{"available":{"version":"2026.38.0"},"step":"downloading"}"#
        XCTAssertEqual(OtaStep(try JSON.decoder().decode(Vehicle.OTA.self, from: Data(json.utf8)))?.line, "2026.38.0 · Downloading")
        let zero = #"{"available":{"version":"0.0.0"},"step":"downloading"}"#
        XCTAssertEqual(OtaStep(try JSON.decoder().decode(Vehicle.OTA.self, from: Data(zero.utf8)))?.line, "Downloading")
    }
}
