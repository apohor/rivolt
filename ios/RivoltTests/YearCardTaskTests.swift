import SwiftUI
import XCTest
@testable import Rivolt

private final class YearStub: URLProtocol {
    nonisolated(unsafe) static var body = Data()
    nonisolated(unsafe) static var paths: [String] = []
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.paths.append(request.url?.path ?? "")
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class YearToken: TokenStore, @unchecked Sendable {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

/// The card is an empty view until its answer arrives, so the screen holding it loads the answer (a .task on an empty Group never
/// starts; found in the 0.2.75 gate) and an empty card must not add a gap to the Money stack.
@MainActor
final class YearCardTaskTests: XCTestCase {
    private func client() throws -> APIClient {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [YearStub.self]
        YearStub.paths = []
        YearStub.body = try Fixture.data("year_in_rivolt")
        return APIClient(baseURL: URL(string: "https://example.invalid")!, tokens: YearToken(), session: URLSession(configuration: c))
    }

    func testTheAnswerIsAskedForInJanuary() async throws {
        let a = await YearCardLogic.load(client: try client(), isDemo: false, now: JSON.parseDate("2027-01-05T12:00:00Z")!, hidden: 0)
        XCTAssertEqual(a?.model, "R1S")
        XCTAssertEqual(YearStub.paths, ["/api/year-in-rivolt"])
    }

    func testNothingIsAskedOutsideJanuaryInTheDemoOrOnceHidden() async throws {
        let c = try client()
        let jan = JSON.parseDate("2027-01-05T12:00:00Z")!
        let o1 = await YearCardLogic.load(client: c, isDemo: false, now: JSON.parseDate("2026-10-30T12:00:00Z")!, hidden: 0)
        let o2 = await YearCardLogic.load(client: c, isDemo: true, now: jan, hidden: 0)
        let o3 = await YearCardLogic.load(client: c, isDemo: false, now: jan, hidden: 2026)
        XCTAssertNil(o1); XCTAssertNil(o2); XCTAssertNil(o3)
        XCTAssertTrue(YearStub.paths.isEmpty, "seen: \(YearStub.paths)")
    }

    func testAnEmptyCardTakesNoSpaceInAStack() throws {
        let c = URLSessionConfiguration.ephemeral
        let state = AppState(tokens: YearToken(), defaults: try XCTUnwrap(UserDefaults(suiteName: "year-gap-\(UUID().uuidString)")), session: URLSession(configuration: c))
        func height<V: View>(_ v: V) -> CGFloat {
            UIHostingController(rootView: v.environment(state)).sizeThatFits(in: CGSize(width: 393, height: 2000)).height
        }
        let without = height(VStack(spacing: 16) { Text("a"); Text("b") })
        let with = height(VStack(spacing: 16) { Text("a"); YearCardView(answer: nil); Text("b") })
        XCTAssertEqual(with, without, accuracy: 0.5)
    }
}
