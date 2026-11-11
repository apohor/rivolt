import XCTest

/// The weather labels the planner's map really draws, with the Weather button on. The labels' text is in the accessibility tree whether or not the map
/// drew them (the itinerary has it too), so these read what a UI-test launch adds: an element where each label the map drew sits ("drawn-weather-label",
/// from the map's own rendered features). None was drawn while the wind arrows and the minus sign had no glyphs in the app.
final class PlannerWeatherLabelsUITests: XCTestCase {
    private static var fixtureDirectories: String {
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        return [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
    }

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = Self.fixtureDirectories
        app.launch()
        return app
    }

    private func open(_ name: String, in app: XCUIApplication) -> XCUIElement {
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20), "the Plan tab is there")
        plan.tap()
        let trip = app.buttons[name].firstMatch
        XCTAssertTrue(trip.waitForExistence(timeout: 20), "the saved trip \(name) is listed")
        trip.tap()
        let map = app.otherElements.matching(NSPredicate(format: "label BEGINSWITH 'Map of the planned route'")).firstMatch
        XCTAssertTrue(map.waitForExistence(timeout: 20), "the map is there")
        return map
    }

    /// At least one weather label drawn, and where it is drawn is on the map, on screen.
    private func expectDrawnLabel(_ map: XCUIElement, in app: XCUIApplication, _ why: String, file: StaticString = #filePath, line: UInt = #line) {
        let drawn = app.descendants(matching: .any).matching(identifier: "drawn-weather-label").firstMatch
        XCTAssertTrue(drawn.waitForExistence(timeout: 20), "a weather label is drawn on the map: \(why)", file: file, line: line)
        guard drawn.exists else { return }
        let at = CGPoint(x: drawn.frame.midX, y: drawn.frame.midY)
        XCTAssertTrue(map.frame.contains(at), "the label (\(drawn.label)) at \(at) is on the map \(map.frame): \(why)", file: file, line: line)
        XCTAssertTrue(app.windows.firstMatch.frame.contains(at), "and on screen: \(why)", file: file, line: line)
    }

    func testTheOneRouteMapDrawsItsWeatherLabels() throws {
        let app = launch()
        let map = open("Seattle to Spokane", in: app)
        expectDrawnLabel(map, in: app, "one route, Weather on")
    }

    func testTheTwoDayMapDrawsItsWeatherLabelsOnEachView() throws {
        let app = launch()
        let map = open("Two days to Spokane", in: app)
        expectDrawnLabel(map, in: app, "the whole trip")
        // The camera fits the trip below the layer chips, so no label is drawn under them.
        let fast = app.buttons["Fast"].firstMatch
        XCTAssertTrue(fast.waitForExistence(timeout: 10))
        let drawn = app.descendants(matching: .any).matching(identifier: "drawn-weather-label")
        for i in 0..<drawn.count {
            XCTAssertGreaterThan(drawn.element(boundBy: i).frame.midY, fast.frame.maxY, "\(drawn.element(boundBy: i).label) is below the chips")
        }
        for chip in ["Day 1", "Day 2"] {
            let button = app.buttons[chip].firstMatch
            XCTAssertTrue(button.waitForExistence(timeout: 10), "\(chip) is there")
            button.tap()
            expectDrawnLabel(map, in: app, chip)
        }
    }
}
