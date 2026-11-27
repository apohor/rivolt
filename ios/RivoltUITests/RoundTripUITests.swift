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
        let toggle = app.buttons["legs-toggle"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 20), "the legs are one row until opened")
        XCTAssertTrue(toggle.label.contains("3 legs, parked 13 minutes, 19 minutes"), "the row says the stops: \(toggle.label)")
        // The open choice is kept on the device, so a leftover from an earlier run is closed first.
        if (toggle.value as? String) == "Legs shown" { toggle.tap() }
        XCTAssertFalse(app.staticTexts["Leg 1"].firstMatch.exists, "collapsed until tapped")
        toggle.tap()
        for n in 1...3 {
            // Wait on the text the row shows. The leg-N identifier is not an "other" element (each poll of a missing element dumps the whole
            // hierarchy, and three legs cost 20 s each before the fallback passed), so the wait is on what is there.
            XCTAssertTrue(app.staticTexts["Leg \(n)"].firstMatch.waitForExistence(timeout: 20), "leg \(n) is listed")
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
