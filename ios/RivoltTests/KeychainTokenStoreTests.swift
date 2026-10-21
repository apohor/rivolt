import XCTest
@testable import Rivolt

final class KeychainTokenStoreTests: XCTestCase {
    /// Its own service, so a test never touches a real session.
    private let store = KeychainTokenStore(service: "dev.rivolt.session.tests")
    private let a = URL(string: "https://rivolt.dev")!
    private let b = URL(string: "https://rivolt.example.com")!

    override func tearDownWithError() throws {
        try store.deleteToken(for: a)
        try store.deleteToken(for: b)
    }

    func testRoundTripUpdateAndDelete() throws {
        XCTAssertNil(try store.token(for: a))
        try store.setToken("t1", for: a)
        XCTAssertEqual(try store.token(for: a), "t1")
        try store.setToken("t2", for: a)
        XCTAssertEqual(try store.token(for: a), "t2")
        try store.deleteToken(for: a)
        XCTAssertNil(try store.token(for: a))
        try store.deleteToken(for: a) // deleting nothing is fine
    }

    /// A token issued by one server is never returned for another.
    func testTokensAreKeptPerServer() throws {
        try store.setToken("for-a", for: a)
        XCTAssertNil(try store.token(for: b))
        try store.setToken("for-b", for: b)
        XCTAssertEqual(try store.token(for: a), "for-a")
        XCTAssertEqual(try store.token(for: b), "for-b")
        XCTAssertEqual(try store.token(for: URL(string: "https://RIVOLT.dev")!), "for-a")
    }
}
