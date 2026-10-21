import XCTest
@testable import Rivolt

final class PKCETests: XCTestCase {
    /// RFC 7636, Appendix B.
    func testChallengeMatchesTheRFCExample() {
        XCTAssertEqual(
            PKCE.challenge(for: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
            "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
        )
    }

    func testGeneratedVerifierIsBase64URLOfThirtyTwoBytes() throws {
        let pair = try PKCE.generate()
        XCTAssertEqual(pair.verifier.count, 43)
        let allowed = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_")
        XCTAssertTrue(pair.verifier.unicodeScalars.allSatisfy(allowed.contains))
        XCTAssertEqual(pair.challenge, PKCE.challenge(for: pair.verifier))
        XCTAssertFalse(pair.challenge.contains("="))
    }

    func testEachPairIsFresh() throws {
        XCTAssertNotEqual(try PKCE.generate().verifier, try PKCE.generate().verifier)
    }
}
