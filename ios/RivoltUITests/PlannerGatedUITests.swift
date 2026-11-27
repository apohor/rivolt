import XCTest

/// A new free account on the Plan tab: planning is Pro, so a card with the tier fact takes the place of the form (no price, no link, no button
/// that can only fail), and the saved trips stay below. When the account buys Pro the form comes back without a restart. Every call is answered
/// from fixtures (Rivolt/App/UITestFixtures.swift, `-uitest-planner-gated`).
final class PlannerGatedUITests: XCTestCase {
    func testGatedPlanShowsTheTierFactInsteadOfTheFormAndProBringsTheFormBack() throws {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        let bought = NSTemporaryDirectory() + "rivolt-uitest-bought-pro-\(UUID().uuidString)"
        defer { try? FileManager.default.removeItem(atPath: bought) }
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchEnvironment["RIVOLT_UITEST_BOUGHT_PRO_FILE"] = bought
        app.launchArguments += ["-uitest-planner-gated"]
        app.launch()
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20), "the Plan tab is there")
        plan.tap()
        let card = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "part of the Pro tier")).firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 20), "the tier fact is shown")
        XCTAssertTrue(card.label.contains("saved earlier"), "this account has saved trips, so the saved-trips line is there")
        XCTAssertFalse(app.buttons["Plan the trip"].exists, "no button that can only fail")
        XCTAssertTrue(app.buttons["Seattle to Spokane"].firstMatch.waitForExistence(timeout: 10), "the saved trips stay listed")
        let a = XCTAttachment(screenshot: app.screenshot()); a.name = "plan-gated"; a.lifetime = .keepAlways; add(a)

        // The account buys Pro (in the app or on the web); the tab was visited, so it is kept alive: leaving and coming back reads /me again.
        XCTAssertTrue(FileManager.default.createFile(atPath: bought, contents: Data()))
        app.buttons["Now"].firstMatch.tap()
        plan.tap()
        XCTAssertTrue(app.buttons["Plan the trip"].firstMatch.waitForExistence(timeout: 20), "the form is back without a restart")
        XCTAssertFalse(card.exists, "the tier fact is gone")
    }
}
