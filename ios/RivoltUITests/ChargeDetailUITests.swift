import XCTest

/// A charge opened from Activity, driven like a person would with every call answered from fixtures (Rivolt/App/UITestFixtures.swift): the screen says what
/// the charge was (its kind, how long, the battery before and after, the energy, the peak power and what it cost), and the way back to the list works.
final class ChargeDetailUITests: XCTestCase {
    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path,
                                                          here.deletingLastPathComponent().appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launch()
        return app
    }

    private func label(_ s: String, in app: XCUIApplication) -> XCUIElement {
        app.otherElements.matching(NSPredicate(format: "label == %@", s)).firstMatch
    }

    func testAFastChargeShowsItsFiguresAndGoesBackToTheList() {
        let app = launch()
        let tab = app.buttons["Activity"].firstMatch
        XCTAssertTrue(tab.waitForExistence(timeout: 20), "the Activity tab is there")
        tab.tap()
        let row = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Charge,' AND label CONTAINS 'Fast charger'")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "the fast charge is listed")
        row.tap()
        XCTAssertTrue(app.staticTexts["Fast charge"].firstMatch.waitForExistence(timeout: 20), "the charge opens with its kind as the title")
        XCTAssertTrue(label("Duration, 41 minutes", in: app).waitForExistence(timeout: 10), "how long")
        XCTAssertTrue(label("SoC, 18 percent to 74 percent", in: app).exists, "the battery before and after, in that order")
        XCTAssertTrue(label("Energy, 71.3 kilowatt hours", in: app).exists, "the energy added")
        XCTAssertTrue(label("Max kilowatts, 198.0 kilowatts", in: app).exists, "the peak power")
        XCTAssertTrue(label("Cost, $23.41. billed", in: app).exists, "what it cost, and that it was billed")
        let back = app.buttons["all charges"].firstMatch
        XCTAssertTrue(back.exists)
        back.tap()
        XCTAssertTrue(row.waitForExistence(timeout: 10), "back on the list, the charge is still there")
        XCTAssertFalse(app.staticTexts["Fast charge"].firstMatch.exists, "the detail is gone")
    }
}
