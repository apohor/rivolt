import XCTest
@testable import Rivolt

/// Every request carries the build number and nothing else that names the install.
final class AppBuildHeaderTests: XCTestCase {
    private final class Recorder: URLProtocol, @unchecked Sendable {
        nonisolated(unsafe) static var seen: [URLRequest] = []
        override class func canInit(with request: URLRequest) -> Bool { true }
        override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
        override func startLoading() {
            Self.seen.append(request)
            let r = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: r, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data("[]".utf8))
            client?.urlProtocolDidFinishLoading(self)
        }
        override func stopLoading() {}
    }

    private struct Token: TokenStore {
        func token(for server: URL) throws -> String? { "t" }
        func setToken(_ token: String, for server: URL) throws {}
        func deleteToken(for server: URL) throws {}
    }

    func testEveryRequestCarriesTheBuildNumberOnly() async throws {
        Recorder.seen = []
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [Recorder.self]
        let client = APIClient(baseURL: URL(string: "https://example.invalid")!, tokens: Token(), session: URLSession(configuration: config))
        _ = try? await client.vehicles()
        _ = try? await client.appConfig()
        XCTAssertEqual(Recorder.seen.count, 2)
        XCTAssertGreaterThan(AppBuild.number, 0, "the test host has a build number")
        for req in Recorder.seen {
            XCTAssertEqual(req.value(forHTTPHeaderField: "X-Rivolt-App-Build"), String(AppBuild.number), "\(req.url?.path ?? "")")
            let names = (req.allHTTPHeaderFields ?? [:]).keys.map { $0.lowercased() }
            XCTAssertEqual(names.filter { $0.hasPrefix("x-") }, ["x-rivolt-app-build"], "no other identifying header")
        }
        XCTAssertNil(Recorder.seen[1].value(forHTTPHeaderField: "Authorization"), "the unauthenticated call still sends no token, build header or not")
    }
}
