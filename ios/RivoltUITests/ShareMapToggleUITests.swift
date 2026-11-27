import XCTest

/// The trip share sheet's "Include map" switch (design-preview key share_map, answered from fixtures): off every time the sheet opens, however
/// it was left the time before.
final class ShareMapToggleUITests: XCTestCase {
    func testIncludeMapIsOffAgainWhenTheSheetIsReopened() {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path,
                                                          here.deletingLastPathComponent().appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += ["-uitest-share-map"]
        app.launch()
        let tab = app.buttons["Activity"].firstMatch
        XCTAssertTrue(tab.waitForExistence(timeout: 20), "the Activity tab is there")
        tab.tap()
        let row = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Drive,'")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "a drive is listed")
        row.tap()

        let share = app.buttons["Share this trip"].firstMatch
        XCTAssertTrue(share.waitForExistence(timeout: 20), "the drive has a Share button")
        share.tap()
        let map = app.switches.matching(NSPredicate(format: "label BEGINSWITH %@", "Include map")).firstMatch
        XCTAssertTrue(map.waitForExistence(timeout: 20), "the map switch is offered once the route is in")
        XCTAssertEqual(map.value as? String, "0", "off on the first open")
        map.tap()
        // Wait on a query that includes the value, not on a predicate over one resolved element: the query is re-resolved on every poll, so a
        // switch the sheet re-created while its card redraws (the map is drawn after the tap) is found, where a held element read it stale.
        XCTAssertTrue(mapSwitch(app, value: "1").waitForExistence(timeout: 20), "turned on; switches now: \(switches(app))")
        app.buttons["Cancel"].firstMatch.tap()
        // The sheet is gone before the Share button is tapped again: during the dismissal the button is in the tree but the tap is lost,
        // and the old sheet's switch (still on) would be read as the reopened one's.
        XCTAssertTrue(map.waitForNonExistence(timeout: 20), "the sheet is dismissed")

        XCTAssertTrue(share.waitForExistence(timeout: 10), "back on the drive")
        share.tap()
        // On a loaded runner the second tap can land while the page is still settling after the dismissal and present nothing (21:59Z run: the
        // switch never came in 20 s, while the same test passed 10 of 10 here and on the next main run). One more tap if the sheet is not up.
        if !map.waitForExistence(timeout: 8) { share.tap() }
        XCTAssertTrue(map.waitForExistence(timeout: 20), "the map switch is offered again on the second open")
        XCTAssertTrue(mapSwitch(app, value: "0").waitForExistence(timeout: 20), "off again on every open; switches now: \(switches(app))")
    }

    private func mapSwitch(_ app: XCUIApplication, value: String) -> XCUIElement {
        app.switches.matching(NSPredicate(format: "label BEGINSWITH %@ AND value == %@", "Include map", value)).firstMatch
    }

    /// For a failure message: every switch on screen with its value.
    private func switches(_ app: XCUIApplication) -> String {
        app.switches.allElementsBoundByIndex.map { "\($0.label.prefix(30)) = \($0.value as? String ?? "?")" }.joined(separator: "; ")
    }
}
