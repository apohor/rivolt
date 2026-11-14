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

    func testTheNowHeaderHasNoMoreMenuAndPullToRefreshStays() {
        let app = launch()
        let profile = app.buttons["settings-button"].firstMatch
        XCTAssertTrue(profile.waitForExistence(timeout: 20))
        XCTAssertFalse(app.buttons["More"].exists, "the refresh menu is gone: refreshing is a pull on the screen")
        // The screen's scroll view still takes the pull (it is .refreshable) and the bar is untouched after it.
        let scroll = app.scrollViews.firstMatch
        XCTAssertTrue(scroll.waitForExistence(timeout: 10))
        scroll.swipeDown()
        XCTAssertTrue(profile.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["More"].exists)
    }

    /// With the Now redesign the sun/moon switch is on the right, next to the person icon, and nothing in the header or the title row sits on top of anything else.
    func testTheNowV2HeaderAndTitleRowDoNotOverlap() {
        let app = launch(["-uitest-now-v2"])
        let profile = app.buttons["settings-button"].firstMatch
        XCTAssertTrue(profile.waitForExistence(timeout: 20))
        let theme = app.buttons["theme-switch"].firstMatch
        XCTAssertTrue(theme.waitForExistence(timeout: 10), "the sun/moon switch is in the top bar")
        XCTAssertFalse(app.buttons["More"].exists, "no More menu")
        XCTAssertFalse(theme.frame.intersects(profile.frame), "the switch and the person icon are apart")
        XCTAssertLessThanOrEqual(theme.frame.maxX, profile.frame.minX + 1, "the switch is left of the person icon, beside it")
        XCTAssertLessThan(profile.frame.minX - theme.frame.maxX, 12, "next to the person icon, not out by the wordmark")
        let wordmark = app.staticTexts["Rivolt"].firstMatch
        XCTAssertTrue(wordmark.waitForExistence(timeout: 10), "the wordmark is in the top bar")
        XCTAssertFalse(wordmark.frame.intersects(theme.frame), "the wordmark and the switch are apart")
        XCTAssertFalse(wordmark.frame.intersects(profile.frame), "the wordmark and the person icon are apart")
        let picker = app.buttons["Switch vehicle"].firstMatch
        let pill = app.descendants(matching: .any)["now-chip"].firstMatch
        XCTAssertTrue(picker.waitForExistence(timeout: 10), "the truck picker is there")
        XCTAssertTrue(pill.waitForExistence(timeout: 10), "the state pill is there")
        XCTAssertFalse(picker.frame.intersects(pill.frame), "the truck picker and the state pill do not overlap")
        // The pill's text sits inside the pill with room to spare, even when it wraps to two lines.
        let text = pill.staticTexts.firstMatch
        XCTAssertTrue(text.exists)
        XCTAssertTrue(pill.frame.insetBy(dx: 4, dy: 4).contains(text.frame), "the pill's text is inside the pill's border: \(text.frame) in \(pill.frame)")
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
