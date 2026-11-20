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
        XCTAssertEqual(map.value as? String, "1", "turned on")
        app.buttons["Cancel"].firstMatch.tap()

        XCTAssertTrue(share.waitForExistence(timeout: 10), "back on the drive")
        share.tap()
        XCTAssertTrue(map.waitForExistence(timeout: 20))
        XCTAssertEqual(map.value as? String, "0", "off again on every open")
    }
}
