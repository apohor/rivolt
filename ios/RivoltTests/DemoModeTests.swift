import SwiftUI
import XCTest
@testable import Rivolt

/// Records what a request carried and answers with a fixed status (or fails, when nothing is set).
private final class DemoStub: URLProtocol {
    nonisolated(unsafe) static var status = 200
    nonisolated(unsafe) static var body = Data("[]".utf8)
    nonisolated(unsafe) static var seen: [URLRequest] = []

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.seen.append(request)
        let resp = HTTPURLResponse(url: request.url!, statusCode: Self.status, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    static func reset(status: Int = 200, body: String = "[]") {
        Self.status = status
        Self.body = Data(body.utf8)
        Self.seen = []
    }
}

/// Counts every touch of the token store: the demo must make none.
private final class TokenSpy: TokenStore, @unchecked Sendable {
    private let lock = NSLock()
    private var _touches = 0
    var touches: Int { lock.withLock { _touches } }
    func token(for server: URL) throws -> String? { lock.withLock { _touches += 1 }; return "should-never-be-sent" }
    func setToken(_ token: String, for server: URL) throws { lock.withLock { _touches += 1 } }
    func deleteToken(for server: URL) throws { lock.withLock { _touches += 1 } }
}

final class DemoClientTests: XCTestCase {
    private func session() -> URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [DemoStub.self]
        return URLSession(configuration: c)
    }

    func testTheDemoGoesToItsOwnFixedAddressWithNoCredentials() async throws {
        DemoStub.reset()
        _ = try await APIClient.demo(session: session()).vehicles()
        let req = try XCTUnwrap(DemoStub.seen.first)
        XCTAssertEqual(req.url?.host, "demo.rivolt.dev")
        XCTAssertEqual(req.url?.scheme, "https")
        XCTAssertNil(req.value(forHTTPHeaderField: "Authorization"), "no sign-in goes to the demo")
        XCTAssertNil(req.value(forHTTPHeaderField: "Cookie"), "no cookie goes to the demo")
        XCTAssertEqual(DemoMode.baseURL.absoluteString, "https://demo.rivolt.dev")
    }

    func testTheDemoRefusesEveryWriteBeforeItReachesTheNetwork() async {
        DemoStub.reset()
        let client = APIClient.demo(session: session())
        do {
            try await client.dismissMilestone(vehicleID: "v", key: "k")
            XCTFail("a write went out from the demo")
        } catch {
            XCTAssertEqual(UserFacingError.words(error).isEmpty, false)
        }
        XCTAssertTrue(DemoStub.seen.isEmpty, "nothing was sent")
    }

    func testAPathTheDemoDoesNotServeReadsAsNotHereNeverAsAnEndedSignIn() async throws {
        DemoStub.reset(status: 401, body: #"{"error":"unauthorized"}"#)
        let demo = APIClient.demo(session: session())
        // An optional card's read: 404 is "not here" and the card is left out.
        let drain = try await demo.drain(vehicleID: "v")
        XCTAssertNil(drain)
        DemoStub.reset(status: 403)
        XCTAssertNil(try await demo.drain(vehicleID: "v"))
        // The same answer to a real client is still an ended sign-in.
        DemoStub.reset(status: 401, body: #"{"error":"unauthorized"}"#)
        let real = APIClient(baseURL: URL(string: "https://rivolt.dev")!, tokens: TokenSpy(), session: session())
        do {
            _ = try await real.drain(vehicleID: "v")
            XCTFail("a 401 should throw for a signed-in client")
        } catch APIError.server(let status, _) {
            XCTAssertEqual(status, 401)
        }
    }

    func testTheWordsOnTheSignInScreenAndTheAboutSheet() {
        XCTAssertEqual(DemoMode.caption, "Sample truck · example data")
        XCTAssertEqual(DemoMode.about, "A sample truck with made-up data. Nothing here is saved or sent to Rivian. Sign in to see your own truck.")
        XCTAssertNil(try? NoTokenStore().token(for: DemoMode.baseURL) ?? nil)
        XCTAssertThrowsError(try NoTokenStore().setToken("x", for: DemoMode.baseURL))
    }
}

@MainActor
final class DemoStateTests: XCTestCase {
    private func state(_ spy: TokenSpy) throws -> AppState {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "demo-tests-\(UUID().uuidString)"))
        let s = AppState(tokens: spy, defaults: defaults)
        s.setPreviewSignIn(.available)
        return s
    }

    func testOpeningAndLeavingTheDemoTouchesNoToken() throws {
        let spy = TokenSpy()
        let s = try state(spy)
        XCTAssertEqual(s.phase, .loggedOut)
        s.enterDemo()
        XCTAssertEqual(s.phase, .demo)
        XCTAssertTrue(s.isDemo)
        s.leaveDemo()
        XCTAssertEqual(s.phase, .loggedOut)
        XCTAssertFalse(s.isDemo)
        XCTAssertEqual(spy.touches, 0, "the demo reads and writes no sign-in")
    }

    func testSignInFromInsideTheDemoLeavesItWithoutEndingAnySession() async throws {
        let spy = TokenSpy()
        let s = try state(spy)
        s.enterDemo()
        await s.logout()
        XCTAssertEqual(s.phase, .loggedOut)
        XCTAssertEqual(spy.touches, 0, "no token was looked at or deleted")
    }

    func testTheDemoOpensOnlyFromTheSignInScreen() throws {
        let s = try state(TokenSpy())
        s.enterDemo()
        s.enterDemo()
        XCTAssertEqual(s.phase, .demo)
        s.leaveDemo()
        s.leaveDemo()
        XCTAssertEqual(s.phase, .loggedOut)
    }
}

@MainActor
final class DemoSnapshotTests: XCTestCase {
    private func render(_ name: String, _ view: some View) throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let r = ImageRenderer(content: view.frame(width: 393).background(Theme.background).environment(\.colorScheme, scheme))
            r.scale = 3
            let image = try XCTUnwrap(r.uiImage, "\(name) did not render")
            XCTAssertGreaterThan(image.size.height, 100)
            let png = try XCTUnwrap(image.pngData())
            let file = "\(name)-\(scheme == .dark ? "dark" : "light").png"
            let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
            attachment.name = file
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
            }
        }
    }

    func testRenderTheSignInScreenWithTheDemoButtonAndTheAboutSheet() throws {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "demo-snap-\(UUID().uuidString)"))
        let s = AppState(tokens: TokenSpy(), defaults: defaults)
        s.setPreviewSignIn(.available)
        try render("login-demo", LoginView().environment(s))
        s.enterDemo()
        try render("demo-about", DemoAboutSheet().environment(s).frame(height: 360))
    }
}
