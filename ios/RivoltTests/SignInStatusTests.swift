import XCTest
@testable import Rivolt

/// What the sign-in button knows before it is tapped: whether the server can do app sign-in.
final class SignInStatusTests: XCTestCase {
    private func config(login: Bool, minBuild: Int? = nil) -> AppConfig {
        AppConfig(server: "rivolt", apiVersion: 1, appLogin: login, minIosBuild: minBuild)
    }

    func testAServerWithAppSignInIsAvailable() {
        XCTAssertEqual(AppState.SignInStatus.from(.success(config(login: true)), appBuild: 5), .available)
    }

    func testAServerWithoutAppSignInDisablesTheButtonAndSaysWhy() {
        let s = AppState.SignInStatus.from(.success(config(login: false)), appBuild: 5)
        XCTAssertEqual(s, .unavailable(.notOnThisServer))
        XCTAssertEqual(s.unavailableReason?.errorDescription, "This server doesn't support signing in from the app yet.")
    }

    func testATooNewServerAndANonRivoltAddress() {
        XCTAssertEqual(AppState.SignInStatus.from(.success(config(login: true, minBuild: 9)), appBuild: 5), .unavailable(.updateApp))
        XCTAssertEqual(AppState.SignInStatus.from(.success(AppConfig(server: "nextcloud", apiVersion: 1, appLogin: true, minIosBuild: nil)), appBuild: 5), .unavailable(.notRivolt))
    }

    func testAMissingEndpointOrAnUnreadableAnswer() {
        XCTAssertEqual(AppState.SignInStatus.from(.failure(APIError.server(status: 404, message: "nf")), appBuild: 5), .unavailable(.notOnThisServer))
        XCTAssertEqual(AppState.SignInStatus.from(.failure(APIError.decoding(URLError(.cannotParseResponse))), appBuild: 5), .unavailable(.notRivolt))
    }

    func testNoAnswerLeavesTheButtonEnabledSoTheTapSaysWhy() {
        XCTAssertEqual(AppState.SignInStatus.from(.failure(URLError(.notConnectedToInternet)), appBuild: 5), .unknown)
        XCTAssertEqual(AppState.SignInStatus.from(.failure(APIError.server(status: 500, message: "x")), appBuild: 5), .unknown)
        XCTAssertNil(AppState.SignInStatus.unknown.unavailableReason)
    }
}
