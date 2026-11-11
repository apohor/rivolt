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

    /// Each day's chip frames that day (two days whose lines are apart: Seattle to Ellensburg, then on to Spokane). Opened as saved, and planned
    /// from a one-day trip by picking a stay, the way a person gets there: the map then switches from the one route to the days.
    func testEachDayChipFramesThatDay() throws {
        let app = launch()
        let map = open("Two days, apart", in: app)
        expectEachDayFramed(map, in: app)
    }

    func testEachDayChipFramesThatDayAfterPickingAStay() throws {
        let app = launch()
        let map = open("Long way to Spokane", in: app)
        expectDrawnLabel(map, in: app, "the one route")
        let find = app.buttons["Find a place to sleep"].firstMatch
        XCTAssertTrue(find.waitForExistence(timeout: 10), "the long drive offers a night")
        reveal(find, in: app)
        find.tap()
        let stay = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Example Hotel'")).firstMatch
        XCTAssertTrue(stay.waitForExistence(timeout: 10), "the stay is listed")
        reveal(stay, in: app)
        stay.tap()
        XCTAssertTrue(app.buttons["Day 1"].firstMatch.waitForExistence(timeout: 20), "the two days are planned")
        expectEachDayFramed(map, in: app)
    }

    /// Scrolls the plan's sheet until `element` can be tapped.
    private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<8 where !element.isHittable {
            app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85))
                .press(forDuration: 0.05, thenDragTo: app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45)))
        }
    }

    /// The labels the map draws, once the camera has settled (they stop changing).
    private func settledLabels(_ app: XCUIApplication) -> [XCUIElement] {
        var last: [String] = []
        for _ in 0..<12 {
            let now = app.descendants(matching: .any).matching(identifier: "drawn-weather-label").allElementsBoundByIndex
            let labels = now.map { "\($0.label)@\(Int($0.frame.midX)),\(Int($0.frame.midY))" }
            if labels == last, !labels.isEmpty { return now }
            last = labels
            Thread.sleep(forTimeInterval: 0.5)
        }
        return app.descendants(matching: .any).matching(identifier: "drawn-weather-label").allElementsBoundByIndex
    }

    /// On the whole trip the westmost label is Day 1's first leg and the eastmost Day 2's last: Day 1 must not draw the eastmost, Day 2 not the westmost.
    private func expectEachDayFramed(_ map: XCUIElement, in app: XCUIApplication, file: StaticString = #filePath, line: UInt = #line) {
        expectDrawnLabel(map, in: app, "the whole trip", file: file, line: line)
        let whole = settledLabels(app).sorted { $0.frame.midX < $1.frame.midX }.map(\.label)
        XCTAssertGreaterThanOrEqual(whole.count, 4, "the whole trip draws a label on each of the four legs: \(whole)", file: file, line: line)
        guard let west = whole.first, let east = whole.last else { return }
        for (chip, other) in [("Day 1", east), ("Day 2", west)] {
            let button = app.buttons[chip].firstMatch
            XCTAssertTrue(button.waitForExistence(timeout: 10), "\(chip) is there", file: file, line: line)
            button.tap()
            expectDrawnLabel(map, in: app, chip, file: file, line: line)
            let drawn = settledLabels(app).map(\.label)
            XCTAssertFalse(drawn.contains(other), "\(chip) is framed on its own day: it drew \(drawn), with \(other) from the other day", file: file, line: line)
        }
    }
}
