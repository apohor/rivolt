import XCTest
@testable import Rivolt

final class LiveDestinationTests: XCTestCase {
    /// Plainly fake places near (10, 10); a precision-6 line through three of them.
    private func encode6(_ pts: [(Double, Double)]) -> String {
        var out = "", pl = 0, pn = 0
        func put(_ v: Int) {
            var u = v < 0 ? ~(v << 1) : v << 1
            while u >= 0x20 { out.append(Character(UnicodeScalar(UInt8((0x20 | (u & 0x1f)) + 63)))); u >>= 5 }
            out.append(Character(UnicodeScalar(UInt8(u + 63))))
        }
        for (la, lo) in pts {
            let a = Int((la * 1e6).rounded()), b = Int((lo * 1e6).rounded())
            put(a - pl); put(b - pn); pl = a; pn = b
        }
        return out
    }

    private func decode(_ json: String) throws -> LiveDestination {
        try JSON.decoder().decode(LiveDestination.self, from: Data(json.utf8))
    }

    private let arrive: (Date) -> String = { _ in "5:40 PM" }

    func testTheRowSaysMilesLeftAndTheArrivalTime() throws {
        let d = try decode(#"{"name":"Work","lat":10.02,"lon":10.03,"eta":"2026-11-16T17:40:00Z","miles_left":12.4}"#)
        XCTAssertEqual(d.line(metric: false, arrive: arrive), "To Work: 12 mi left, arrive 5:40 PM")
    }

    func testTheRowFollowsTheUnitSettingAndKeepsADecimalUnderTen() throws {
        let far = try decode(#"{"name":"Work","lat":10.02,"lon":10.03,"eta":"2026-11-16T17:40:00Z","miles_left":10}"#)
        XCTAssertEqual(far.line(metric: true, arrive: arrive), "To Work: 16 km left, arrive 5:40 PM")
        let near = try decode(#"{"name":"Work","lat":10.02,"lon":10.03,"eta":"2026-11-16T17:40:00Z","miles_left":4.26}"#)
        XCTAssertEqual(near.line(metric: false, arrive: arrive), "To Work: 4.3 mi left, arrive 5:40 PM")
        XCTAssertFalse(near.line(metric: true, arrive: arrive).contains(" mi"))
    }

    func testTheRowLeavesTheDistanceOutWhenTheCarDoesNotKnowIt() throws {
        let d = try decode(#"{"name":"Work","lat":10.02,"lon":10.03,"eta":"2026-11-16T17:40:00Z"}"#)
        XCTAssertEqual(d.line(metric: false, arrive: arrive), "To Work: arrive 5:40 PM")
    }

    func testTheRouteDecodesAtPrecisionSixAndAMissingOneIsEmpty() throws {
        let pts: [(Double, Double)] = [(10, 10), (10.0123, 10.0456), (10.02, 10.03)]
        let d = try decode(#"{"name":"W","lat":10.02,"lon":10.03,"eta":"2026-11-16T17:40:00Z","route":"\#(encode6(pts).replacingOccurrences(of: "\\", with: "\\\\"))","route_precision":6}"#)
        XCTAssertEqual(d.routePoints.count, 3)
        for (i, p) in d.routePoints.enumerated() {
            XCTAssertEqual(p.lat, pts[i].0, accuracy: 1e-6)
            XCTAssertEqual(p.lon, pts[i].1, accuracy: 1e-6)
        }
        let none = try decode(#"{"name":"W","lat":10.02,"lon":10.03,"eta":"2026-11-16T17:40:00Z"}"#)
        XCTAssertEqual(none.routePoints, [])
    }

    func testAZeroFixIsNotDrawn() throws {
        XCTAssertFalse(try decode(#"{"name":"W","lat":0,"lon":0,"eta":"2026-11-16T17:40:00Z"}"#).hasFix)
        XCTAssertTrue(try decode(#"{"name":"W","lat":10.02,"lon":10.03,"eta":"2026-11-16T17:40:00Z"}"#).hasFix)
    }

    // MARK: the client's three answers

    private final class Answer: URLProtocol, @unchecked Sendable {
        nonisolated(unsafe) static var status = 200
        nonisolated(unsafe) static var body = ""
        override class func canInit(with request: URLRequest) -> Bool { true }
        override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
        override func startLoading() {
            let r = HTTPURLResponse(url: request.url!, statusCode: Self.status, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: r, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(Self.body.utf8))
            client?.urlProtocolDidFinishLoading(self)
        }
        override func stopLoading() {}
    }

    private struct Token: TokenStore {
        func token(for server: URL) throws -> String? { "t" }
        func setToken(_ token: String, for server: URL) throws {}
        func deleteToken(for server: URL) throws {}
    }

    private func read(_ status: Int, _ body: String) async throws -> LiveDestinationRead {
        Answer.status = status
        Answer.body = body
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [Answer.self]
        let client = APIClient(baseURL: URL(string: "https://example.invalid")!, tokens: Token(), session: URLSession(configuration: config))
        return try await client.liveDestination(id: "v1")
    }

    func testTheClientMapsTheThreeAnswers() async throws {
        let trip = try await read(200, #"{"name":"Work","lat":10.02,"lon":10.03,"eta":"2026-11-16T17:40:00Z","miles_left":12.4}"#)
        guard case .trip(let d) = trip else { return XCTFail("a 200 is a trip") }
        XCTAssertEqual(d.name, "Work")
        let none = try await read(204, "")
        XCTAssertEqual(none, .noTrip)
        let off = try await read(404, #"{"error":"not found"}"#)
        XCTAssertEqual(off, .off)
        do {
            _ = try await read(503, #"{"error":"try again"}"#)
            XCTFail("a 503 is retryable, not an answer")
        } catch {}
    }
}
