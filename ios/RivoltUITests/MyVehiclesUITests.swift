import XCTest

/// The screen for an account with no vehicle yet, in the three states the server can give (looking, none, reauth), driven like a person would with every call
/// answered from fixtures (Rivolt/App/UITestFixtures.swift, RIVOLT_UITEST_NO_TRUCK): each says its own words, and only the ones with something to do have the
/// way to do it.
final class MyVehiclesUITests: XCTestCase {
    private func launch(_ state: String) -> XCUIApplication {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path,
                                                          here.deletingLastPathComponent().appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchEnvironment["RIVOLT_UITEST_NO_TRUCK"] = state
        app.launch()
        return app
    }

    private func text(_ s: String, in app: XCUIApplication) -> XCUIElement { app.staticTexts[s].firstMatch }

    func testLookingSaysItIsLookingAndOffersNothingToConnect() {
        let app = launch("looking")
        XCTAssertTrue(text("Looking for your vehicles", in: app).waitForExistence(timeout: 20))
        XCTAssertTrue(text("Rivolt is checking your Rivian account. This usually takes a few minutes, and your vehicles show up here when it is done.", in: app).exists)
        XCTAssertFalse(app.buttons["Reconnect Rivian"].exists)
        XCTAssertFalse(app.links["Connect on the web"].exists, "looking: nothing to connect, only to wait")
        XCTAssertTrue(app.buttons["Check again"].exists)
        XCTAssertTrue(app.buttons["See the sample truck"].exists)
    }

    func testNoVehiclesSaysSoAndPointsToTheWeb() {
        let app = launch("none")
        XCTAssertTrue(text("No vehicles on this Rivian account yet", in: app).waitForExistence(timeout: 20))
        XCTAssertTrue(text("Your Rivian account is connected, but it lists no vehicle. Rivolt looks again about once a day.", in: app).exists)
        XCTAssertTrue(app.links["Connect on the web"].exists || app.buttons["Connect on the web"].exists, "none: another account is connected on the web")
        XCTAssertTrue(app.buttons["Check again"].exists)
    }

    func testReauthAsksToReconnect() {
        let app = launch("reauth")
        XCTAssertTrue(text("Reconnect your Rivian", in: app).waitForExistence(timeout: 20))
        XCTAssertTrue(text("Rivian needs you to sign in again before Rivolt can look for your vehicles.", in: app).exists)
        XCTAssertTrue(app.links["Reconnect Rivian"].exists || app.buttons["Reconnect Rivian"].exists, "reauth: the way to sign in again")
        XCTAssertFalse(text("Looking for your vehicles", in: app).exists)
    }
}
