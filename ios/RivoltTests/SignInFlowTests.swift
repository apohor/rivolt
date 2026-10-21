import XCTest
@testable import Rivolt

/// A one-time code shaped like the server's (base64url, 43 characters).
private let code = "Q2hhcmdlZC10by04MC1wZXJjZW50LWF0LWhvbWUtb2s"

/// An in-memory token store, one token per server origin like the Keychain one.
private final class MemoryTokenStore: TokenStore, @unchecked Sendable {
    var tokens: [String: String] = [:]
    func token(for server: URL) throws -> String? { tokens[ServerURL.origin(of: server)] }
    func setToken(_ token: String, for server: URL) throws { tokens[ServerURL.origin(of: server)] = token }
    func deleteToken(for server: URL) throws { tokens[ServerURL.origin(of: server)] = nil }
}

/// Stands in for the browser: records what it was opened with and returns a
/// canned redirect built from the start URL's challenge.
@MainActor
private final class FakeBrowser: WebAuthenticator {
    var started: [(start: URL, callback: SignInCallback)] = []
    var answer: (URL) throws -> URL

    init(answer: @escaping (URL) throws -> URL) {
        self.answer = answer
    }

    func authenticate(start: URL, callback: SignInCallback) async throws -> URL {
        started.append((start, callback))
        return try answer(start)
    }
}

/// Answers the client's requests in place of the server.
private final class StubServer: URLProtocol {
    nonisolated(unsafe) static var requests: [URLRequest] = []
    nonisolated(unsafe) static var bodies: [Data] = []
    nonisolated(unsafe) static var respond: (URLRequest) -> (Int, Data) = { _ in (500, Data()) }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.requests.append(request)
        Self.bodies.append(Self.body(of: request))
        let (status, data) = Self.respond(request)
        let resp = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil,
                                   headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    /// URLSession hands a protocol the body as a stream.
    private static func body(of request: URLRequest) -> Data {
        if let d = request.httpBody { return d }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var out = Data()
        var buf = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let n = stream.read(&buf, maxLength: buf.count)
            if n <= 0 { break }
            out.append(buf, count: n)
        }
        return out
    }

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubServer.self]
        return URLSession(configuration: config)
    }
}

@MainActor
final class SignInFlowTests: XCTestCase {
    private let rivoltDev = URL(string: "https://rivolt.dev")!
    private let selfHosted = URL(string: "https://rivolt.example.com")!

    override func setUp() {
        super.setUp()
        StubServer.requests = []
        StubServer.bodies = []
        StubServer.respond = { _ in
            (200, (try? Fixture.data("app_token")) ?? Data())
        }
    }

    private func flow(server: URL, tokens: TokenStore, browser: FakeBrowser) -> SignInFlow {
        SignInFlow(
            server: server,
            client: APIClient(baseURL: server, tokens: tokens, session: StubServer.session()),
            tokens: tokens,
            authenticator: browser,
            deviceName: "iPhone"
        )
    }

    // MARK: Callback choice

    func testRivoltDevNeverUsesTheScheme() throws {
        for raw in ["https://rivolt.dev", "rivolt.dev", "https://RIVOLT.DEV", "https://rivolt.dev:443", "https://rivolt.dev."] {
            let server = (try? ServerURL.normalize(raw)) ?? URL(string: raw)!
            let callback = SignInCallback.forServer(server)
            XCTAssertEqual(callback, .https(host: "rivolt.dev", path: "/app/callback"), raw)
            if case .customScheme = callback { XCTFail("rivolt.dev must never use the scheme: \(raw)") }
        }
    }

    func testOtherServersUseTheScheme() throws {
        for raw in ["https://preview.rivolt.dev", "https://rivolt.example.com", "https://rivolt.dev.example.com"] {
            XCTAssertEqual(SignInCallback.forServer(URL(string: raw)!), .customScheme("rivolt"), raw)
        }
    }

    func testCallbackMatchesExactly() {
        let https = SignInCallback.https(host: "rivolt.dev", path: "/app/callback")
        XCTAssertTrue(https.matches(URL(string: "https://rivolt.dev/app/callback?code=x")!))
        for bad in [
            "http://rivolt.dev/app/callback?code=x",
            "https://evil.example/app/callback?code=x",
            "https://rivolt.dev/app/callback/?code=x",
            "https://rivolt.dev/app/callbackx?code=x",
            "https://rivolt.dev:8443/app/callback?code=x",
            "https://user@rivolt.dev/app/callback?code=x",
            "rivolt://auth?code=x",
        ] {
            XCTAssertFalse(https.matches(URL(string: bad)!), bad)
        }
        let scheme = SignInCallback.customScheme("rivolt")
        XCTAssertTrue(scheme.matches(URL(string: "rivolt://auth?code=x")!))
        for bad in ["rivolt://evil?code=x", "rivolt://auth/x?code=x", "other://auth?code=x", "https://rivolt.dev/app/callback?code=x"] {
            XCTAssertFalse(scheme.matches(URL(string: bad)!), bad)
        }
    }

    func testOutcome() {
        let cb = SignInCallback.https(host: "rivolt.dev", path: "/app/callback")
        let base = "https://rivolt.dev/app/callback"
        XCTAssertEqual(cb.outcome(of: URL(string: "\(base)?code=\(code)")!), .code(code))
        XCTAssertEqual(cb.outcome(of: URL(string: "\(base)?error=access_denied")!), .denied)
        XCTAssertEqual(cb.outcome(of: URL(string: "\(base)?error=invalid_request")!), .failed)
        XCTAssertEqual(cb.outcome(of: URL(string: base)!), .failed)
        XCTAssertEqual(cb.outcome(of: URL(string: "\(base)?code=\(code)&error=access_denied")!), .failed)
        XCTAssertEqual(cb.outcome(of: URL(string: "\(base)?code=\(code)&code=\(code)")!), .failed)
        XCTAssertEqual(cb.outcome(of: URL(string: "\(base)?code=short")!), .failed)
        XCTAssertEqual(cb.outcome(of: URL(string: "\(base)?code=\(code)%2F..%2F")!), .failed)
        XCTAssertEqual(cb.outcome(of: URL(string: "https://evil.example/app/callback?code=\(code)")!), .failed)
    }

    // MARK: The whole flow

    func testSignInStoresTheTokenForTheServer() async throws {
        let tokens = MemoryTokenStore()
        tokens.tokens[ServerURL.origin(of: rivoltDev)] = "old-token"
        let browser = FakeBrowser { _ in URL(string: "https://rivolt.dev/app/callback?code=\(code)")! }

        let username = try await flow(server: rivoltDev, tokens: tokens, browser: browser).run()

        XCTAssertEqual(username, "sam")
        XCTAssertEqual(tokens.tokens, ["https://rivolt.dev": "rvt_app_fixture_not_a_real_token_0000000000000"])

        // The browser opened the start page with an S256 challenge.
        let start = try XCTUnwrap(browser.started.first)
        XCTAssertEqual(start.callback, .https(host: "rivolt.dev", path: "/app/callback"))
        let q = try XCTUnwrap(URLComponents(url: start.start, resolvingAgainstBaseURL: false))
        XCTAssertEqual(q.host, "rivolt.dev")
        XCTAssertEqual(q.path, "/api/auth/app/start")
        XCTAssertEqual(q.queryItems?.first { $0.name == "code_challenge_method" }?.value, "S256")
        let challenge = try XCTUnwrap(q.queryItems?.first { $0.name == "code_challenge" }?.value)

        // The token request: the code, the verifier behind that challenge and
        // the device name, to the same server, without the old token.
        XCTAssertEqual(StubServer.requests.count, 1)
        let req = try XCTUnwrap(StubServer.requests.first)
        XCTAssertEqual(req.httpMethod, "POST")
        XCTAssertEqual(req.url?.absoluteString, "https://rivolt.dev/api/auth/app/token")
        XCTAssertNil(req.value(forHTTPHeaderField: "Authorization"))
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: StubServer.bodies[0]) as? [String: String])
        XCTAssertEqual(Set(body.keys), ["code", "code_verifier", "device_name"])
        XCTAssertEqual(body["code"], code)
        XCTAssertEqual(body["device_name"], "iPhone")
        XCTAssertEqual(PKCE.challenge(for: try XCTUnwrap(body["code_verifier"])), challenge)
    }

    func testSelfHostedListensOnTheScheme() async throws {
        let tokens = MemoryTokenStore()
        let browser = FakeBrowser { _ in URL(string: "rivolt://auth?code=\(code)")! }
        _ = try await flow(server: selfHosted, tokens: tokens, browser: browser).run()
        XCTAssertEqual(browser.started.first?.callback, .customScheme("rivolt"))
        XCTAssertEqual(StubServer.requests.first?.url?.host, "rivolt.example.com")
        XCTAssertEqual(Array(tokens.tokens.keys), ["https://rivolt.example.com"])
    }

    func testEachSignInHasItsOwnChallenge() async throws {
        let browser = FakeBrowser { _ in URL(string: "https://rivolt.dev/app/callback?code=\(code)")! }
        _ = try await flow(server: rivoltDev, tokens: MemoryTokenStore(), browser: browser).run()
        _ = try await flow(server: rivoltDev, tokens: MemoryTokenStore(), browser: browser).run()
        let challenges = browser.started.compactMap {
            URLComponents(url: $0.start, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "code_challenge" }?.value
        }
        XCTAssertEqual(challenges.count, 2)
        XCTAssertNotEqual(challenges[0], challenges[1])
    }

    func testExpiredCodeStoresNothing() async {
        StubServer.respond = { _ in (400, Data(#"{"error":"invalid_grant"}"#.utf8)) }
        let tokens = MemoryTokenStore()
        let browser = FakeBrowser { _ in URL(string: "https://rivolt.dev/app/callback?code=\(code)")! }
        await assertThrows(SignInError.expired) { try await self.flow(server: self.rivoltDev, tokens: tokens, browser: browser).run() }
        XCTAssertTrue(tokens.tokens.isEmpty)
    }

    func testDeniedOnTheConsentPage() async {
        let tokens = MemoryTokenStore()
        let browser = FakeBrowser { _ in URL(string: "https://rivolt.dev/app/callback?error=access_denied")! }
        await assertThrows(SignInError.denied) { try await self.flow(server: self.rivoltDev, tokens: tokens, browser: browser).run() }
        XCTAssertTrue(StubServer.requests.isEmpty)
        XCTAssertTrue(tokens.tokens.isEmpty)
    }

    func testWrongCallbackNeverReachesTheServer() async {
        let tokens = MemoryTokenStore()
        // A scheme redirect for rivolt.dev, which only listens on https.
        let browser = FakeBrowser { _ in URL(string: "rivolt://auth?code=\(code)")! }
        await assertThrows(SignInError.failed) { try await self.flow(server: self.rivoltDev, tokens: tokens, browser: browser).run() }
        XCTAssertTrue(StubServer.requests.isEmpty)
        XCTAssertTrue(tokens.tokens.isEmpty)
    }

    func testCancelInTheBrowser() async {
        let tokens = MemoryTokenStore()
        let browser = FakeBrowser { _ in throw SignInError.cancelled }
        await assertThrows(SignInError.cancelled) { try await self.flow(server: self.rivoltDev, tokens: tokens, browser: browser).run() }
        XCTAssertTrue(StubServer.requests.isEmpty)
        XCTAssertNil(SignInError.cancelled.errorDescription, "a cancel shows no message")
    }

    func testTokenFixtureDecodes() throws {
        let t = try Fixture.decode(AppToken.self, "app_token")
        XCTAssertEqual(t.tokenType, "Bearer")
        XCTAssertEqual(t.user.username, "sam")
    }

    private func assertThrows(_ expected: SignInError, _ body: () async throws -> Void,
                              file: StaticString = #filePath, line: UInt = #line) async {
        do {
            try await body()
            XCTFail("expected \(expected)", file: file, line: line)
        } catch let error as SignInError {
            XCTAssertEqual(error, expected, file: file, line: line)
        } catch {
            XCTFail("expected \(expected), got \(error)", file: file, line: line)
        }
    }
}
