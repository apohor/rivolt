import XCTest
@testable import Rivolt

private final class Reply: URLProtocol {
    nonisolated(unsafe) static var status = 401
    nonisolated(unsafe) static var body = ""
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let resp = HTTPURLResponse(url: request.url!, statusCode: Self.status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(Self.body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private struct Token: TokenStore {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

/// A 401 that names a `class` is Rivian's (reconnect it on the web), not a dead session of this phone: the server's contract says
/// so for the planner and every Rivian-backed route, and the words must not tell the person to sign in again.
final class RivianReauthTests: XCTestCase {
    private func call(_ status: Int, _ body: String) async -> Error? {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [Reply.self]
        Reply.status = status; Reply.body = body
        let api = APIClient(baseURL: URL(string: "https://rivolt.dev")!, tokens: Token(), session: URLSession(configuration: c))
        do { _ = try await api.vehicles(); return nil } catch { return error }
    }

    func testA401WithAClassIsRivianNotTheSession() async throws {
        let failure = await call(401, #"{"error":"Rivian sign-in needs attention","class":"user_action","reason":"needs_reauth"}"#)
        let e = try XCTUnwrap(failure)
        guard case APIError.rivianReauth = e else { return XCTFail("\(e)") }
        let words = UserFacingError.words(e)
        XCTAssertEqual(words, "Rivian needs to be reconnected. Do that in Rivolt on the web, then try again.")
        XCTAssertFalse(words.contains("sign-in has ended"), "the phone's own sign-in is fine")
    }

    func testABare401IsStillADeadSession() async throws {
        for body in ["unauthorized", #"{"error":"unauthorized"}"#] {
            let failure = await call(401, body)
            let e = try XCTUnwrap(failure)
            guard case APIError.server(let status, _) = e else { return XCTFail("\(e)") }
            XCTAssertEqual(status, 401)
            XCTAssertEqual(UserFacingError.words(e), "Your sign-in has ended. Sign in again.")
        }
    }

    func testOtherClassedAnswersKeepTheirStatus() async throws {
        let failure = await call(429, #"{"error":"quota","class":"plan_quota","retry_after_sec":600}"#)
        let e = try XCTUnwrap(failure)
        guard case APIError.server(let status, _) = e else { return XCTFail("\(e)") }
        XCTAssertEqual(status, 429)
    }
}
