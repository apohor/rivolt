#if DEBUG && QA_SIGNIN
import XCTest
@testable import Rivolt

/// The QA sign-in hook signs in to the preview server only, and only when asked on the launch line.
final class QASignInTests: XCTestCase {
    private final class Store: TokenStore, @unchecked Sendable {
        var saved: [URL: String] = [:]
        func token(for server: URL) throws -> String? { saved[server] }
        func setToken(_ token: String, for server: URL) throws { saved[server] = token }
        func deleteToken(for server: URL) throws { saved[server] = nil }
    }

    private func run(_ env: [String: String]) -> (QASignIn.Outcome, Store, UserDefaults) {
        let store = Store()
        let defaults = UserDefaults(suiteName: "qa-signin-\(UUID().uuidString)")!
        let outcome = QASignIn.apply(environment: env, tokens: store, defaults: defaults, serverKey: "rivolt.server_url")
        return (outcome, store, defaults)
    }

    func testThePreviewServerWithATokenSignsIn() {
        let (outcome, store, defaults) = run(["RIVOLT_QA_SERVER": "https://preview.rivolt.dev", "RIVOLT_QA_TOKEN": "t"])
        XCTAssertEqual(outcome, .signedIn)
        XCTAssertEqual(store.saved[URL(string: "https://preview.rivolt.dev")!], "t")
        XCTAssertEqual(defaults.string(forKey: "rivolt.server_url"), "https://preview.rivolt.dev")
    }

    func testAnyOtherServerIsRefusedAndNothingIsStored() {
        for server in ["https://rivolt.dev", "https://demo.rivolt.dev", "https://preview.rivolt.dev.evil.example", "http://preview.rivolt.dev", "preview.rivolt.dev/x"] {
            let (outcome, store, defaults) = run(["RIVOLT_QA_SERVER": server, "RIVOLT_QA_TOKEN": "t"])
            XCTAssertNotEqual(outcome, .signedIn, server)
            XCTAssertTrue(store.saved.isEmpty, server)
            XCTAssertNil(defaults.string(forKey: "rivolt.server_url"), server)
        }
    }

    func testWithoutBothVariablesNothingHappens() {
        XCTAssertEqual(run([:]).0, .notAsked)
        XCTAssertEqual(run(["RIVOLT_QA_SERVER": "https://preview.rivolt.dev"]).0, .notAsked)
        XCTAssertEqual(run(["RIVOLT_QA_TOKEN": "t"]).0, .notAsked)
        XCTAssertEqual(run(["RIVOLT_QA_SERVER": "https://preview.rivolt.dev", "RIVOLT_QA_TOKEN": ""]).0, .notAsked)
    }
}
#endif
