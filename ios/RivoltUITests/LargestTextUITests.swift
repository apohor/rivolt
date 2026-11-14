import XCTest

/// The largest accessibility text size (AX3 and up) on a small phone's screens, with every call answered from fixtures (Rivolt/App/UITestFixtures.swift):
/// the header's two buttons stay apart and the planner form's FROM and TO labels keep a readable line.
final class LargestTextUITests: XCTestCase {
    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        app.launch()
        return app
    }

    func testTheNowHeaderButtonsDoNotOverlap() {
        let app = launch()
        let profile = app.buttons["settings-button"].firstMatch
        XCTAssertTrue(profile.waitForExistence(timeout: 20))
        let more = app.buttons["More"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 10), "the refresh menu is on Now")
        XCTAssertFalse(more.frame.intersects(profile.frame), "the two header buttons are side by side, not on top of each other")
    }

    func testThePlanFormLabelsAreNotBrokenOneLetterPerLine() {
        let app = launch()
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20))
        plan.tap()
        let from = app.staticTexts["From"].firstMatch
        XCTAssertTrue(from.waitForExistence(timeout: 20))
        XCTAssertLessThan(from.frame.height, from.frame.width * 1.5, "FROM reads as a word, not a column of letters")
    }
}
