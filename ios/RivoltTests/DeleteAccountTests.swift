import SwiftUI
import XCTest
@testable import Rivolt

private final class DeleteStub: URLProtocol {
    nonisolated(unsafe) static var status = 200
    nonisolated(unsafe) static var seen: [URLRequest] = []
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.seen.append(request)
        let resp = HTTPURLResponse(url: request.url!, statusCode: Self.status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"id":"u","username":"sam"}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class HeldToken: TokenStore, @unchecked Sendable {
    private let lock = NSLock()
    private var value: String? = "tok"
    var held: String? { lock.withLock { value } }
    func token(for server: URL) throws -> String? { held }
    func setToken(_ token: String, for server: URL) throws { lock.withLock { value = token } }
    func deleteToken(for server: URL) throws { lock.withLock { value = nil } }
}

@MainActor
final class DeleteAccountTests: XCTestCase {
    private func signedIn(_ tokens: HeldToken) async throws -> AppState {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [DeleteStub.self]
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "delete-tests-\(UUID().uuidString)"))
        let s = AppState(tokens: tokens, defaults: defaults, session: URLSession(configuration: c))
        DeleteStub.status = 200
        DeleteStub.seen = []
        await s.bootstrap()
        return s
    }

    func testThePageIsTheWebDangerZone() throws {
        let url = try XCTUnwrap(DeleteAccountSheet.pageURL(server: URL(string: "https://rivolt.dev")!))
        XCTAssertEqual(url.absoluteString, "https://rivolt.dev/settings?tab=data#danger")
    }

    func testNothingHappensUnlessThePersonWentToTheWeb() async throws {
        let tokens = HeldToken()
        let s = try await signedIn(tokens)
        DeleteStub.status = 401
        await s.recheckAfterDeletion()
        XCTAssertEqual(s.phase, .loggedIn(username: "sam"))
        XCTAssertNil(s.signInNotice)
    }

    func testStillSignedInAfterTheWebPageStaysSignedIn() async throws {
        let tokens = HeldToken()
        let s = try await signedIn(tokens)
        s.accountDeletionStarted()
        await s.recheckAfterDeletion()
        XCTAssertEqual(s.phase, .loggedIn(username: "sam"))
        XCTAssertNotNil(tokens.held)
    }

    func testAnUnreachableServerDoesNotSignOut() async throws {
        let tokens = HeldToken()
        let s = try await signedIn(tokens)
        s.accountDeletionStarted()
        DeleteStub.status = 503
        await s.recheckAfterDeletion()
        XCTAssertEqual(s.phase, .loggedIn(username: "sam"))
    }

    func testADeletedAccountSignsOutLocallyAndSaysSo() async throws {
        let tokens = HeldToken()
        let s = try await signedIn(tokens)
        s.accountDeletionStarted()
        DeleteStub.status = 401
        DeleteStub.seen = []
        await s.recheckAfterDeletion()
        XCTAssertEqual(s.phase, .loggedOut)
        XCTAssertEqual(s.signInNotice, "Your account was deleted.")
        XCTAssertNil(tokens.held)
        XCTAssertTrue(DeleteStub.seen.allSatisfy { $0.httpMethod == "GET" }, "no sign-out call goes to a server that has already ended the session")
        s.clearSignInNotice()
        XCTAssertNil(s.signInNotice)
    }
}
