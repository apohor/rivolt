import XCTest

/// The planner's forecast radar on the one-route map (design key plan_forecast_map, Pro): the scrubber on the sheet's edge, its label and its one line.
/// The fixture forecast has the trip's first four hours (16:00 to 19:00 UTC on 2027-01-12); the trip runs 16:00 to 20:55, so its last hour is past the
/// horizon. The map's tiles are not served here, so this reads the controls and the words, not the picture.
final class ForecastRadarUITests: XCTestCase {
    private static var fixtureDirectories: String {
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        return [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
    }

    private func launch(forecast: Bool) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = Self.fixtureDirectories
        if forecast { app.launchArguments += ["-uitest-forecast"] }
        app.launch()
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20), "the Plan tab is there")
        plan.tap()
        let trip = app.buttons["Seattle to Spokane"].firstMatch
        XCTAssertTrue(trip.waitForExistence(timeout: 20), "the saved trip is listed")
        trip.tap()
        return app
    }

    /// The still of the screen, where the person running this asked for it (TEST_RUNNER_RIVOLT_STILLS_DIR), and always as an attachment.
    private func still(_ app: XCUIApplication, _ name: String) {
        let shot = app.screenshot()
        let a = XCTAttachment(screenshot: shot)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["RIVOLT_STILLS_DIR"] {
            try? shot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }

    func testTheScrubberStepsThroughTheTripsHoursAndSaysWhenTheForecastEnds() throws {
        let app = launch(forecast: true)
        let strip = app.descendants(matching: .any).matching(identifier: "radar-strip").firstMatch
        XCTAssertTrue(strip.waitForExistence(timeout: 20), "the strip is there")
        XCTAssertTrue(strip.label.contains("NOAA RRFS radar, Forecast for"), "it is the forecast's strip: \(strip.label)")
        let slider = app.sliders["Forecast hour"].firstMatch
        XCTAssertTrue(slider.waitForExistence(timeout: 10), "the scrubber is there")
        XCTAssertFalse(app.staticTexts["The forecast doesn't reach this hour."].exists, "the departure hour is inside the forecast")
        still(app, "forecast-ios-first-hour")
        let first = strip.label
        // To the last hour: past the horizon, so the one line and no layer.
        slider.adjust(toNormalizedSliderPosition: 1)
        XCTAssertTrue(app.staticTexts["The forecast doesn't reach this hour."].waitForExistence(timeout: 10), "past the horizon the map says so")
        XCTAssertNotEqual(strip.label, first, "the label follows the thumb")
        still(app, "forecast-ios-past-horizon")
        slider.adjust(toNormalizedSliderPosition: 0)
        XCTAssertTrue(app.staticTexts["The forecast doesn't reach this hour."].waitForNonExistence(timeout: 10), "back at the departure hour the line goes")
    }

    func testWithoutTheKeyThereIsNoScrubber() throws {
        let app = launch(forecast: false)
        let map = app.otherElements.matching(NSPredicate(format: "label BEGINSWITH 'Map of the planned route'")).firstMatch
        XCTAssertTrue(map.waitForExistence(timeout: 20), "the plan's map is there")
        sleep(3)
        XCTAssertFalse(app.sliders["Forecast hour"].exists, "the key is off: no scrubber")
    }
}
