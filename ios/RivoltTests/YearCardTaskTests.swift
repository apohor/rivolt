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

/// The card is an empty view until its answer arrives, and a .task on an empty Group never starts (found in the 0.2.75 gate).
@MainActor
final class YearCardTaskTests: XCTestCase {
    private func host(now: Date) throws -> UIWindow {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [YearStub.self]
        YearStub.paths = []
        YearStub.body = try Fixture.data("year_in_rivolt")
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "year-task-\(UUID().uuidString)"))
        let state = AppState(tokens: YearToken(), defaults: defaults, session: URLSession(configuration: c))
        let window = UIWindow(frame: CGRect(x: 0, y: 0, width: 393, height: 852))
        window.rootViewController = UIHostingController(rootView: NavigationStack { ScrollView { VStack(spacing: 16) { YearCardView(now: now) } } }.environment(state))
        window.makeKeyAndVisible()
        return window
    }

    func testTheAnswerIsAskedForInJanuaryWhileTheCardIsStillEmpty() async throws {
        let window = try host(now: JSON.parseDate("2027-01-05T12:00:00Z")!)
        for _ in 0..<50 where !YearStub.paths.contains("/api/year-in-rivolt") { try await Task.sleep(nanoseconds: 100_000_000) }
        XCTAssertTrue(YearStub.paths.contains("/api/year-in-rivolt"), "seen: \(YearStub.paths)")
        window.isHidden = true
    }

    func testNothingIsAskedOutsideJanuary() async throws {
        let window = try host(now: JSON.parseDate("2026-10-30T12:00:00Z")!)
        try await Task.sleep(nanoseconds: 1_000_000_000)
        XCTAssertTrue(YearStub.paths.isEmpty, "seen: \(YearStub.paths)")
        window.isHidden = true
    }
}
