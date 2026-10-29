import SwiftUI
import XCTest
@testable import Rivolt

private final class OnboardStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(#"{"id":"u","username":"sam"}"#.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class OneToken: TokenStore, @unchecked Sendable {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

@MainActor
final class OnboardingTests: XCTestCase {
    private func vehicle() throws -> Vehicle {
        try JSON.decoder().decode(Vehicle.self, from: Data(#"{"id":"v1","vin":"TESTVIN0000000001","name":"R1S","model":"R1S"}"#.utf8))
    }

    private func signedIn() async throws -> AppState {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [OnboardStub.self]
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "onboard-tests-\(UUID().uuidString)"))
        let s = AppState(tokens: OneToken(), defaults: defaults, session: URLSession(configuration: c))
        await s.bootstrap()
        return s
    }

    func testTheConnectPageIsTheWebRivianCard() throws {
        let url = try XCTUnwrap(Onboarding.connectURL(server: URL(string: "https://rivolt.dev")!))
        XCTAssertEqual(url.absoluteString, "https://rivolt.dev/settings?tab=account#rivian")
    }

    func testOnlyAnEmptyListIsNoTruck() throws {
        XCTAssertEqual(Onboarding.gate(vehicles: []), .noTruck)
        XCTAssertEqual(Onboarding.gate(vehicles: [try vehicle()]), .hasTruck)
        XCTAssertEqual(Onboarding.gate(vehicles: nil), .hasTruck, "a failed read shows the app, whose screens say what went wrong")
    }

    func testTheSampleTruckFromAnAccountLeadsBackToTheAccount() async throws {
        let s = try await signedIn()
        XCTAssertEqual(s.phase, .loggedIn(username: "sam"))
        s.enterDemoFromAccount()
        XCTAssertEqual(s.phase, .demo)
        XCTAssertTrue(s.demoResumesAccount)
        s.leaveDemo()
        XCTAssertEqual(s.phase, .loggedIn(username: "sam"))
        XCTAssertFalse(s.demoResumesAccount)
    }

    func testTheDemoFromTheSignInScreenStillLeadsToTheSignInScreen() async throws {
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "onboard-tests-\(UUID().uuidString)"))
        let s = AppState(tokens: NoTokenStore(), defaults: defaults)
        await s.bootstrap()
        XCTAssertEqual(s.phase, .loggedOut)
        s.enterDemoFromAccount()
        XCTAssertNotEqual(s.phase, .demo, "only a signed-in account can open it from here")
        s.enterDemo()
        XCTAssertFalse(s.demoResumesAccount)
        s.leaveDemo()
        XCTAssertEqual(s.phase, .loggedOut)
    }

    func testTheExplanationSaysWhatRivoltDoesWithTheLogin() {
        let e = Onboarding.explanation
        XCTAssertTrue(e.contains("the app never sees your Rivian password"))
        XCTAssertTrue(e.contains("Rivolt stores it nowhere"))
        XCTAssertTrue(e.contains("sends no commands"))
        XCTAssertFalse(e.contains("sign in with your Rivian email and password"), "the old line pointed at the login itself")
    }

    func testTheWordsAreFreeOfBannedOnes() {
        let words = [Onboarding.title, Onboarding.explanation, Onboarding.sampleCaption].joined(separator: " ")
        for banned in ["OK", "Normal", "safe", "secure", "towing", "metered", "exact", "measured"] {
            XCTAssertFalse(words.contains(banned), banned)
        }
    }

    func testRenderTheConnectScreen() throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let view = NoTruckContent(connect: URL(string: "https://rivolt.dev/settings"), onCheck: {}, onSample: {})
                .frame(width: 393).background(Theme.background).environment(\.colorScheme, scheme)
            let r = ImageRenderer(content: view)
            r.scale = 2
            let image = try XCTUnwrap(r.uiImage, "no-truck did not render")
            XCTAssertGreaterThan(image.size.height, 100)
            let png = try XCTUnwrap(image.pngData())
            let file = "no-truck-\(scheme == .dark ? "dark" : "light").png"
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
}
