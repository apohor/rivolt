import XCTest
@testable import Rivolt

private final class GateStub: URLProtocol {
    nonisolated(unsafe) static var fixture = "me_plan_free_planner_pro"
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let resp = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: resp, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: (try? Fixture.data(Self.fixture)) ?? Data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private struct GateToken: TokenStore {
    func token(for server: URL) throws -> String? { "tok" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

/// The Plan tab follows the account: the gate is unknown until /me is read, and a read after a purchase takes it away (every read goes through readMe).
@MainActor
final class PlannerGateFlipTests: XCTestCase {
    func testTheGateIsUnknownThenSetThenLiftedByAProRead() async throws {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [GateStub.self]
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "planner-gate-\(UUID().uuidString)"))
        let s = AppState(tokens: GateToken(), defaults: defaults, session: URLSession(configuration: c))
        XCTAssertNil(s.plannerProOnly, "unknown until the first read")
        GateStub.fixture = "me_plan_free_planner_pro"
        _ = try await s.readMe()
        XCTAssertEqual(s.plannerProOnly, true)
        GateStub.fixture = "me_plan_subscriber"
        _ = try await s.readMe()
        XCTAssertEqual(s.plannerProOnly, false, "the account bought Pro: the form is back without a restart")
        GateStub.fixture = "me_plan_free"
        _ = try await s.readMe()
        XCTAssertEqual(s.plannerProOnly, false, "an older free account is not gated")
    }
}
