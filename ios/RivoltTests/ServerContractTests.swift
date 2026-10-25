import XCTest
@testable import Rivolt

/// The server's own responses (the fixtures Go decodes strictly into its own types: backend's contract tests) decoded by the
/// Swift types the app reads them with, so a drift on either side fails a test.
final class ServerContractTests: XCTestCase {
    func testMeCarriesWhoAndTheAccountsUnits() throws {
        let me = try Fixture.decode(AuthUser.self, "me")
        XCTAssertEqual(me.username, "sample-owner")
        XCTAssertEqual(me.userId, "00000000-0000-4000-8000-000000000001")
        let units = try XCTUnwrap(me.units)
        XCTAssertEqual(units.distanceUnit, "km")
        XCTAssertEqual(units.temperatureUnit, "c")
        XCTAssertEqual(units.distanceSource, "region")
        XCTAssertFalse(units.isUnset)
        XCTAssertNil(units.temperatureForDisplay, "the temperature follows the distance")
    }

    func testANewAccountReadsAsAnAccountToo() throws {
        let me = try Fixture.decode(AuthUser.self, "me_unaccepted")
        XCTAssertEqual(me.username, "sample-new")
        XCTAssertNil(me.units, "the server sends none until it has the setting for this account")
    }

    func testASignedOutAnswerIsNotAnAccount() {
        XCTAssertThrowsError(try Fixture.decode(AuthUser.self, "me_signed_out"), "no username: the app treats it as signed out")
    }

    func testTheUnitsEndpointReadsUnsetAsPlaceholders() throws {
        let u = try Fixture.decode(AccountUnits.self, "settings_units")
        XCTAssertTrue(u.isUnset)
        XCTAssertEqual(u.distanceUnit, "mi")
        XCTAssertEqual(u.temperatureUnit, "f")
        XCTAssertEqual(u.temperatureSource, "follows_distance")
    }

    func testTheAppConfigIsOneThisBuildCanSignInTo() throws {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        let c = try decoder.decode(AppConfig.self, from: Fixture.data("app_config"))
        XCTAssertEqual(c.server, "rivolt")
        XCTAssertEqual(c.verdict(appBuild: c.minIosBuild ?? 0), .ready)
        XCTAssertTrue(c.liveAvailable)
    }

    func testTheErrorAnswersCarryTheWordsTheAppShows() throws {
        for name in ["error_terms_required", "error_not_connected", "error_app_refused"] {
            let obj = try XCTUnwrap(try JSONSerialization.jsonObject(with: Fixture.data(name)) as? [String: Any], name)
            XCTAssertFalse((obj["error"] as? String ?? "").isEmpty, "\(name) has an error string, which APIClient reads as the message")
        }
    }
}
