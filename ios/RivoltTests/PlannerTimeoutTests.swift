import XCTest
@testable import Rivolt

/// The trip planner calls get 60 s / 90 s; everything else keeps the 15 s / 30 s the phone uses on cellular.
final class PlannerTimeoutTests: XCTestCase {
    private final class Recorder: URLProtocol, @unchecked Sendable {
        nonisolated(unsafe) static var seen: [URLRequest] = []
        override class func canInit(with request: URLRequest) -> Bool { true }
        override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
        override func startLoading() {
            Self.seen.append(request)
            let r = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: r, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data("{}".utf8))
            client?.urlProtocolDidFinishLoading(self)
        }
        override func stopLoading() {}
    }

    private struct Token: TokenStore {
        func token(for server: URL) throws -> String? { "t" }
        func setToken(_ token: String, for server: URL) throws {}
        func deleteToken(for server: URL) throws {}
    }

    func testOnlyThePlanCallsGetTheLongRequestTimeout() async throws {
        Recorder.seen = []
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [Recorder.self]
        let client = APIClient(baseURL: URL(string: "https://example.invalid")!, tokens: Token(), session: URLSession(configuration: config))
        let wp = [TripPlanWaypoint(latitude: 41.5, longitude: -81.7, waypointType: "origin"),
                  TripPlanWaypoint(latitude: 39.7, longitude: -105.0, waypointType: "destination")]
        _ = try? await client.planTrip(TripPlanRequest(waypoints: wp))
        _ = try? await client.planMultiday(TripMultidayRequest(
            startAt: "2026-11-12T12:00:00Z", tz: "America/New_York",
            origin: TripPoint(lat: 41.5, lon: -81.7), days: [TripDayRequest(end: TripPoint(lat: 39.7, lon: -105.0))]))
        _ = try? await client.vehicles()
        let byPath = Dictionary(uniqueKeysWithValues: Recorder.seen.map { ($0.url!.path, $0.timeoutInterval) })
        XCTAssertEqual(byPath["/api/trips/plan"], 60)
        XCTAssertEqual(byPath["/api/trips/plan-multiday"], 60)
        XCTAssertEqual(byPath["/api/vehicles"], 15, "other calls keep the 15 s")
    }

    func testTheLongLimitsAreWhatTheServerNeeds() {
        XCTAssertEqual(APIClient.longRequestTimeout, 60)
        XCTAssertEqual(APIClient.longResourceTimeout, 90)
    }
}
