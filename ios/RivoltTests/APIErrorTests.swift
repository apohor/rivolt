import XCTest
@testable import Rivolt

/// First CI test: proves the test target builds against the app module and
/// runs on the simulator. Real coverage (PKCE, Keychain, server config)
/// arrives with the sign-in skeleton.
final class APIErrorTests: XCTestCase {
    func testServerErrorSurfacesTheServerMessage() {
        let err = APIError.server(status: 401, message: "session expired")
        XCTAssertEqual(err.errorDescription, "session expired")
    }

    func testBadURLHasAReadableDescription() {
        XCTAssertEqual(APIError.badURL.errorDescription, "Invalid server URL.")
    }
}
