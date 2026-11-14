import XCTest

/// A merged round trip opened from Activity (fixtures: three real legs and a zero-distance stub the row leaves out): it reads "Round trip, 3 legs",
/// and the detail lists every leg with how long the truck sat between them.
final class RoundTripUITests: XCTestCase {
    func testTheRoundTripListsEveryLegWithItsStops() {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path,
                                                          here.deletingLastPathComponent().appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += ["-uitest-round-trip"]
        app.launch()
        let tab = app.buttons["Activity"].firstMatch
        XCTAssertTrue(tab.waitForExistence(timeout: 20), "the Activity tab is there")
        tab.tap()
        let row = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Drive,' AND label CONTAINS 'Round trip'")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "the round trip is listed")
        XCTAssertTrue(row.label.contains("3 legs"), "three drives, the stub not counted: \(row.label)")
        row.tap()
        XCTAssertTrue(app.staticTexts["Round trip"].firstMatch.waitForExistence(timeout: 20), "it opens as a round trip")
        for n in 1...3 {
            XCTAssertTrue(app.otherElements["leg-\(n)"].firstMatch.waitForExistence(timeout: 20) || app.staticTexts["Leg \(n)"].firstMatch.exists, "leg \(n) is listed")
        }
        XCTAssertFalse(app.staticTexts["Leg 4"].firstMatch.exists, "the stub is not a leg")
        XCTAssertTrue(app.staticTexts["Parked 13 min"].firstMatch.exists, "the first stop")
        XCTAssertTrue(app.staticTexts["Parked 19 min"].firstMatch.exists, "the second stop")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "round-trip-detail"
        shot.lifetime = .keepAlways
        add(shot)
        app.swipeUp()
        let legs = XCTAttachment(screenshot: app.screenshot())
        legs.name = "round-trip-legs"
        legs.lifetime = .keepAlways
        add(legs)
    }
}
