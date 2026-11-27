import XCTest

/// The planner's place search, with the server's answer from a fixture: a hotel taken from the charger list shows its town, its state spelled out and
/// the "EV charging" note under it; a plain place does not. Two characters send nothing, three do.
final class PlaceSearchUITests: XCTestCase {
    private static var fixtureDirectories: String {
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        return [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
    }

    func testAChargerHotelRowNamesItsTownAndSaysEVCharging() throws {
        let app = XCUIApplication()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = Self.fixtureDirectories
        app.launch()
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20), "the Plan tab is there")
        plan.tap()
        let to = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Choose a place")).firstMatch
        XCTAssertTrue(to.waitForExistence(timeout: 20), "the To row is there")
        to.tap()
        let field = app.textFields["A city, or a street and a city"]
        XCTAssertTrue(field.waitForExistence(timeout: 10), "the search box is there")
        field.tap()
        field.typeText("su")
        XCTAssertFalse(app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Super 8")).firstMatch.waitForExistence(timeout: 1.5), "two characters ask nothing")
        field.typeText("per 8")
        let row = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Super 8, Dalhart, Texas, United States")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "the hotel row names the town and the state")
        XCTAssertTrue(row.staticTexts["EV charging"].exists, "and says EV charging")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "place-search-hotel-row"
        shot.lifetime = .keepAlways
        add(shot)
    }
}
