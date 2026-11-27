import XCTest

/// A new free account on the Plan tab: planning is Pro, so a card with the tier fact takes the place of the form (no price, no link, no button
/// that can only fail), and the saved trips stay below. Every call is answered from fixtures (Rivolt/App/UITestFixtures.swift, `-uitest-planner-gated`).
final class PlannerGatedUITests: XCTestCase {
    func testGatedPlanShowsTheTierFactInsteadOfTheForm() throws {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
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
        let shot = app.screenshot()
        let a = XCTAttachment(screenshot: shot); a.name = "plan-gated"; a.lifetime = .keepAlways; add(a)
        try? FileManager.default.createDirectory(atPath: "/tmp/claude-501/shots", withIntermediateDirectories: true)
        try? shot.pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/claude-501/shots/plan-gated.png"))
    }
}
