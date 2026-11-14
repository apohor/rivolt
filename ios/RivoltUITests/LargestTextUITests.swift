import XCTest

/// The largest accessibility text size (AX3 and up) on a small phone's screens, with every call answered from fixtures (Rivolt/App/UITestFixtures.swift):
/// the header's two buttons stay apart and the planner form's FROM and TO labels keep a readable line.
final class LargestTextUITests: XCTestCase {
    private func launch(_ arguments: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += arguments + ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
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

    /// With the Now redesign's top-bar switch and its truck row, nothing in the header or the title row sits on top of anything else.
    func testTheNowV2HeaderAndTitleRowDoNotOverlap() {
        let app = launch(["-uitest-now-v2"])
        let profile = app.buttons["settings-button"].firstMatch
        XCTAssertTrue(profile.waitForExistence(timeout: 20))
        let more = app.buttons["More"].firstMatch
        let theme = app.buttons["theme-switch"].firstMatch
        XCTAssertTrue(theme.waitForExistence(timeout: 10), "the sun/moon switch is in the top bar")
        XCTAssertTrue(more.waitForExistence(timeout: 10))
        XCTAssertFalse(theme.frame.intersects(more.frame), "the switch and More are apart")
        XCTAssertFalse(theme.frame.intersects(profile.frame), "the switch and the person icon are apart")
        XCTAssertFalse(more.frame.intersects(profile.frame))
        let wordmark = app.staticTexts["Rivolt"].firstMatch
        XCTAssertTrue(wordmark.waitForExistence(timeout: 10), "the wordmark is in the top bar")
        XCTAssertFalse(wordmark.frame.intersects(theme.frame), "the wordmark and the switch are apart")
        XCTAssertFalse(wordmark.frame.intersects(more.frame), "the wordmark and More are apart")
        XCTAssertFalse(wordmark.frame.intersects(profile.frame), "the wordmark and the person icon are apart")
        let picker = app.buttons["Switch vehicle"].firstMatch
        let pill = app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'Offline' OR label CONTAINS 'Last seen' OR label CONTAINS 'Charging'")).firstMatch
        XCTAssertTrue(picker.waitForExistence(timeout: 10), "the truck picker is there")
        XCTAssertTrue(pill.waitForExistence(timeout: 10), "the state pill is there")
        XCTAssertFalse(picker.frame.intersects(pill.frame), "the truck picker and the state pill do not overlap")
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
