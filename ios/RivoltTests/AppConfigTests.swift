import XCTest
@testable import Rivolt

final class AppConfigTests: XCTestCase {
    private func decode(_ json: String) throws -> AppConfig {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(AppConfig.self, from: Data(json.utf8))
    }

    func testDecodesTheServerShape() throws {
        let c = try decode(#"{"server":"rivolt","api_version":1,"app_login":true,"push":{"apns":false},"min_ios_build":3}"#)
        XCTAssertEqual(c, AppConfig(server: "rivolt", apiVersion: 1, appLogin: true, minIosBuild: 3, push: .init(apns: false)))
    }

    func testVerdicts() {
        let ready = AppConfig(server: "rivolt", apiVersion: 1, appLogin: true, minIosBuild: 3)
        XCTAssertEqual(ready.verdict(appBuild: 3), .ready)
        XCTAssertEqual(ready.verdict(appBuild: 2), .appTooOld(minimumBuild: 3))
        XCTAssertEqual(AppConfig(server: "rivolt", apiVersion: 1, appLogin: false, minIosBuild: nil).verdict(appBuild: 1), .noAppSignIn)
        XCTAssertEqual(AppConfig(server: "nextcloud", apiVersion: 1, appLogin: true, minIosBuild: nil).verdict(appBuild: 1), .notRivolt)
    }

    func testApnsAvailabilityFollowsTheServer() throws {
        XCTAssertTrue(try decode(#"{"server":"rivolt","api_version":1,"app_login":true,"push":{"apns":true}}"#).apnsAvailable)
        XCTAssertFalse(try decode(#"{"server":"rivolt","api_version":1,"app_login":true,"push":{"apns":false}}"#).apnsAvailable)
        // A server that predates push says nothing about it: no notifications offered.
        XCTAssertFalse(try decode(#"{"server":"rivolt","api_version":1,"app_login":true}"#).apnsAvailable)
    }
}
