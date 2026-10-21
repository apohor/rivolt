import XCTest
@testable import Rivolt

final class ServerURLTests: XCTestCase {
    func testDefaultIsRivoltDev() {
        XCTAssertEqual(ServerURL.default.absoluteString, "https://rivolt.dev")
    }

    func testNormalizesTypedAddresses() throws {
        XCTAssertEqual(try ServerURL.normalize("rivolt.example.com").absoluteString, "https://rivolt.example.com")
        XCTAssertEqual(try ServerURL.normalize("  https://Rivolt.Example.com/ ").absoluteString, "https://rivolt.example.com")
        XCTAssertEqual(try ServerURL.normalize("HTTPS://nas.local:8443").absoluteString, "https://nas.local:8443")
    }

    func testRejectsAnythingButAnHTTPSOrigin() {
        XCTAssertThrowsError(try ServerURL.normalize("")) { XCTAssertEqual($0 as? ServerURLError, .empty) }
        XCTAssertThrowsError(try ServerURL.normalize("http://rivolt.example.com")) { XCTAssertEqual($0 as? ServerURLError, .notHTTPS) }
        XCTAssertThrowsError(try ServerURL.normalize("https://example.com/rivolt")) { XCTAssertEqual($0 as? ServerURLError, .notAnOrigin) }
        XCTAssertThrowsError(try ServerURL.normalize("https://example.com/?next=evil")) { XCTAssertEqual($0 as? ServerURLError, .notAnOrigin) }
        XCTAssertThrowsError(try ServerURL.normalize("https://user:pw@example.com")) { XCTAssertEqual($0 as? ServerURLError, .notAnOrigin) }
    }

    func testOriginKeysIgnoreCaseAndKeepThePort() {
        XCTAssertEqual(ServerURL.origin(of: URL(string: "https://Rivolt.dev")!), "https://rivolt.dev")
        XCTAssertEqual(ServerURL.origin(of: URL(string: "https://nas.local:8443")!), "https://nas.local:8443")
    }
}
