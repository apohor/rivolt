import SwiftUI
import XCTest
@testable import Rivolt

private final class TermsStub: URLProtocol {
    nonisolated(unsafe) static var status = 200
    nonisolated(unsafe) static var body = #"{"username":"sam","terms":{"current":"2","accepted":null,"required":true}}"#
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

private final class TermsToken: TokenStore, @unchecked Sendable {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

@MainActor
final class TermsRequiredTests: XCTestCase {
    private func state() async throws -> AppState {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [TermsStub.self]
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "terms-tests-\(UUID().uuidString)"))
        TermsStub.status = 200
        TermsStub.body = #"{"username":"sam"}"#
        let s = AppState(tokens: TermsToken(), defaults: defaults, session: URLSession(configuration: c))
        await s.bootstrap()
        return s
    }

    func testMeCarriesTheTermsAndAnOldServerCarriesNone() throws {
        let me = try JSON.decoder().decode(AuthUser.self, from: Data(#"{"username":"sam","terms":{"current":"2","accepted":null,"accepted_at":null,"required":true}}"#.utf8))
        XCTAssertEqual(me.terms?.isRequired, true)
        XCTAssertNil(try JSON.decoder().decode(AuthUser.self, from: Data(#"{"username":"sam"}"#.utf8)).terms)
    }

    func testTheGateFollowsMe() async throws {
        let s = try await state()
        XCTAssertFalse(s.termsRequired)
        TermsStub.body = #"{"username":"sam","terms":{"current":"2","accepted":null,"required":true}}"#
        await s.recheckTerms()
        XCTAssertTrue(s.termsRequired)
        TermsStub.body = #"{"username":"sam","terms":{"current":"2","accepted":"2","required":false}}"#
        await s.recheckTerms()
        XCTAssertFalse(s.termsRequired, "after accepting on the web, the check lets the app through")
    }

    func testAnUnreachableServerLeavesTheAnswerAsItWas() async throws {
        let s = try await state()
        TermsStub.body = #"{"username":"sam","terms":{"current":"2","accepted":null,"required":true}}"#
        await s.recheckTerms()
        TermsStub.status = 503
        await s.recheckTerms()
        XCTAssertTrue(s.termsRequired)
    }

    func testADataRouteRefusalRaisesTheGateEvenMidSession() async throws {
        let s = try await state()
        TermsStub.status = 403
        TermsStub.body = #"{"error":"terms_required","current":"2"}"#
        do { _ = try await s.client.vehicles(); XCTFail("403 should throw") } catch APIError.server(let status, let message) {
            XCTAssertEqual(status, 403)
            XCTAssertEqual(message, "terms_required")
        }
        try await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertTrue(s.termsRequired)
    }

    func testTheWordsReplaceTheDeadEnd() {
        let err = APIError.server(status: 403, message: "terms_required")
        XCTAssertEqual(UserFacingError.words(err), "Accept the updated Terms to continue.")
        XCTAssertEqual(UserFacingError.words(APIError.server(status: 403, message: "nope")), "This server doesn't allow that from the app.")
        XCTAssertEqual(ChargingRatesModel.saveError(err), .termsRequired)
        for banned in ["OK", "Normal", "safe", "secure", "towing", "metered", "exact", "measured"] {
            XCTAssertFalse((TermsUpdated.title + TermsUpdated.explanation).contains(banned), banned)
        }
    }

    func testRenderTheTermsScreen() throws {
        Theme.registerFonts()
        for scheme in [ColorScheme.light, .dark] {
            let view = TermsUpdatedContent(open: URL(string: "https://rivolt.dev/")!, onCheck: {})
                .frame(width: 393).background(Theme.background).environment(\.colorScheme, scheme)
            let r = ImageRenderer(content: view)
            r.scale = 2
            let image = try XCTUnwrap(r.uiImage, "terms did not render")
            XCTAssertGreaterThan(image.size.height, 100)
            let png = try XCTUnwrap(image.pngData())
            let file = "terms-updated-\(scheme == .dark ? "dark" : "light").png"
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

final class TermsUpdatedExtrasTests: XCTestCase {
    func testTheScreenNamesTheVersionAndKeepsExportAndDeleteOnTheWeb() {
        XCTAssertEqual(TermsUpdated.updatedOn("2026-10-23"), "Terms updated 2026-10-23")
        XCTAssertNil(TermsUpdated.updatedOn(nil))
        XCTAssertNil(TermsUpdated.updatedOn(""))
        XCTAssertEqual(TermsUpdated.dataURL(URL(string: "https://rivolt.dev")!).absoluteString, "https://rivolt.dev/settings?tab=data")
        XCTAssertEqual(TermsUpdated.dataLine, "Export or delete your data")
    }
}
