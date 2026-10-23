import XCTest
@testable import Rivolt

final class UserFacingErrorTests: XCTestCase {
    func testAFailureIsSaidInPlainWordsNeverWithTheErrorsOwnText() {
        let url = URL(string: "https://rivolt.example.org/api/state/veh-123456")!
        let timeout = URLError(.timedOut, userInfo: [NSURLErrorFailingURLErrorKey: url, NSLocalizedDescriptionKey: "The request to \(url) timed out."])
        XCTAssertTrue(timeout.localizedDescription.contains("veh-123456"), "an error's own text carries the URL: that is why it is not shown")
        let all: [Error] = [
            timeout, URLError(.notConnectedToInternet), URLError(.cannotFindHost), URLError(.badServerResponse),
            APIError.server(status: 503, message: "boom at https://rivolt.example.org for veh-123456"), APIError.server(status: 429, message: "x"),
            APIError.transport("https://rivolt.example.org/api/state/veh-123456 failed"), APIError.badURL,
            APIError.decoding(NSError(domain: "x", code: 1, userInfo: [NSLocalizedDescriptionKey: "secret value"])),
            NSError(domain: "x", code: 1, userInfo: [NSLocalizedDescriptionKey: "secret"]),
        ]
        for e in all {
            let text = UserFacingError.words(e)
            for leak in ["veh-123456", "rivolt.example.org", "http", "secret", "boom", "Error Domain"] {
                XCTAssertFalse(text.contains(leak), "\(e) → \(text)")
            }
            XCTAssertTrue(text.hasSuffix(".") || text.hasSuffix(")."), text)
        }
    }

    func testEachKindHasItsOwnWords() {
        XCTAssertEqual(UserFacingError.words(URLError(.timedOut)), "The server took too long to answer. Try again in a moment.")
        XCTAssertEqual(UserFacingError.words(URLError(.notConnectedToInternet)), "You're offline. Check your connection and try again.")
        XCTAssertEqual(UserFacingError.words(URLError(.cannotConnectToHost)), "Can't reach your Rivolt server. Try again in a moment.")
        XCTAssertEqual(UserFacingError.words(APIError.server(status: 401, message: "")), "Your sign-in has ended. Sign in again.")
        XCTAssertEqual(UserFacingError.words(APIError.server(status: 403, message: "")), "This server doesn't allow that from the app.")
        XCTAssertEqual(UserFacingError.words(APIError.server(status: 429, message: "")), "Too many requests. Wait a minute, then try again.")
        XCTAssertEqual(UserFacingError.words(APIError.server(status: 502, message: "")), "The server had a problem. Try again in a moment.")
        XCTAssertEqual(UserFacingError.words(APIError.server(status: 418, message: "")), "The server couldn't do that (error 418).")
        XCTAssertTrue(UserFacingError.words(APIError.decoding(NSError(domain: "x", code: 1))).hasPrefix("The server's answer wasn't what the app expected"))
    }
}
