import XCTest
@testable import Rivolt

/// The release-notes link: the vehicle list only says a link exists, the link is asked for when tapped, and a failure is
/// one of a few fixed phrases.
final class ReleaseNotesTests: XCTestCase {
    func testTheVehicleListCarriesOnlyWhetherNotesExist() throws {
        let vehicles = try Fixture.decode([Vehicle].self, "vehicles")
        let ota = try XCTUnwrap(vehicles.compactMap(\.ota).first)
        XCTAssertEqual(ota.current?.hasNotes, true)
    }

    func testFailuresAreFixedPhrases() {
        let leaky = "https://docs.s3.amazonaws.com/x.pdf?X-Amz-Security-Token=SECRET"
        XCTAssertEqual(ReleaseNotesLink.message(for: APIError.server(status: 429, message: leaky)), "Try again in a few minutes.")
        XCTAssertEqual(ReleaseNotesLink.message(for: APIError.server(status: 401, message: leaky)), "Reconnect Rivian to open release notes.")
        XCTAssertEqual(ReleaseNotesLink.message(for: APIError.server(status: 404, message: leaky)), "No release notes for this version.")
        for error in [APIError.server(status: 502, message: leaky), APIError.transport(leaky), APIError.badURL, URLError(.timedOut)] as [Error] {
            let text = ReleaseNotesLink.message(for: error)
            XCTAssertEqual(text, "Release notes unavailable right now.")
            XCTAssertFalse(text.contains("SECRET") || text.contains("amazonaws"))
        }
    }
}
