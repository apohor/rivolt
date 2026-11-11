import XCTest

/// The signed-in planner, driven like a person would, with every call answered from fixtures (Rivolt/App/UITestFixtures.swift): each layer chip draws its
/// pins (the map says how many places are drawn along the route in its accessibility value, which is what these read), the two-day map has reachable
/// Day chips, and the (i) opens and closes.
final class PlannerLayerPinsUITests: XCTestCase {
    /// ios/RivoltUITests/Fixtures (the layer stations, the saved trips) and ios/RivoltTests/Fixtures (the unit tests' answers), found from this file's place.
    private static var fixtureDirectories: String {
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        return [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
    }

    /// `largestText` is the largest accessibility text size (AX5), where the layer chips give way to a Layers menu.
    private func launch(largestText: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        if largestText { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = Self.fixtureDirectories
        app.launch()
        return app
    }

    /// Opens the saved trip called `name` on the Plan tab and returns the planner's map.
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

    func testEachChipDrawsItsPins() throws {
        let app = launch()
        let map = open("Seattle to Spokane", in: app)
        expectPins(map, "3 places along the route", "Fast is on to begin with")
        app.buttons["Medium"].firstMatch.tap()
        expectPins(map, "5 places along the route", "Medium adds its two")
        app.buttons["Hotel"].firstMatch.tap()
        expectPins(map, "7 places along the route", "Hotel adds its two")
        app.buttons["Camping"].firstMatch.tap()
        expectPins(map, "8 places along the route", "Camping adds its one")
        app.buttons["Fast"].firstMatch.tap()
        expectPins(map, "5 places along the route", "Fast off takes its three away")
        for chip in ["Medium", "Hotel", "Camping"] { app.buttons[chip].firstMatch.tap() }
        expectNoPins(map, "every chip off: no pins")
    }

    func testATwoDayTripHasReachableDayChipsAndTheLayerPins() throws {
        let app = launch()
        let map = open("Two days to Spokane", in: app)
        expectPins(map, "3 places along the route", "the layers reach the two-day map too (they did not once)")
        for chip in ["Whole trip", "Day 1", "Day 2"] { XCTAssertTrue(app.buttons[chip].firstMatch.waitForExistence(timeout: 10), "\(chip) is there") }
        app.buttons["Day 2"].firstMatch.tap()
        XCTAssertTrue(app.buttons["Day 2"].firstMatch.isSelected, "Day 2 is the one picked")
        XCTAssertFalse(app.buttons["Whole trip"].firstMatch.isSelected)
        expectPins(map, "3 places along the route", "a day still has its pins")
    }

    func testTheInfoButtonOpensAndClosesItsExplanation() throws {
        let app = launch()
        _ = open("Seattle to Spokane", in: app)
        let info = app.buttons["What the charger types mean"].firstMatch
        XCTAssertTrue(info.waitForExistence(timeout: 10))
        info.tap()
        XCTAssertTrue(app.staticTexts["Fast"].firstMatch.waitForExistence(timeout: 5), "the explanation is up")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'DC fast'")).firstMatch.exists, "with a sentence for Fast")
        app.otherElements["PopoverDismissRegion"].firstMatch.tap()
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'DC fast'")).firstMatch.waitForExistence(timeout: 2), "a tap outside closes it")
    }

    func testAtTheLargestTextTheLayersAreOneMenuThatStillDrawsPins() throws {
        let app = launch(largestText: true)
        let map = open("Seattle to Spokane", in: app)
        expectPins(map, "3 places along the route", "Fast is on to begin with, at the largest text too")
        XCTAssertFalse(app.buttons["Medium"].firstMatch.exists, "no chips at this size: they would fill the map")
        let layers = app.buttons["Map layers"].firstMatch
        XCTAssertTrue(layers.waitForExistence(timeout: 10), "one Layers button instead")
        XCTAssertTrue(layers.isHittable, "and a thumb can reach it")
        layers.tap()
        let hotel = app.buttons["Hotel"].firstMatch
        XCTAssertTrue(hotel.waitForExistence(timeout: 5), "the menu has the same switches")
        hotel.tap()
        expectPins(map, "5 places along the route", "Hotel adds its two from the menu")
        XCTAssertTrue((layers.value as? String ?? "").contains("Hotel"), "the button says what is on: \(String(describing: layers.value))")
    }

    func testAtTheLargestTextATwoDayTripsDaysAreReachable() throws {
        let app = launch(largestText: true)
        let map = open("Two days to Spokane", in: app)
        expectPins(map, "3 places along the route", "the layers reach the two-day map at the largest text")
        let day2 = app.buttons["Day 2"].firstMatch
        // The sheet starts down to its title at this size, with the day chips under the tab bar and out of VoiceOver's reach: its handle opens it.
        let handle = app.buttons["Itinerary"].firstMatch
        XCTAssertEqual(handle.value as? String, "peek")
        XCTAssertFalse(day2.exists, "Day 2 is hidden at the peek")
        handle.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).press(forDuration: 0.1, thenDragTo: app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.1)))
        XCTAssertNotEqual(handle.value as? String, "peek", "the handle opened the sheet")
        XCTAssertTrue(day2.waitForExistence(timeout: 10), "Day 2 is in the opened sheet")
        let hittable = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "hittable == true"), object: day2)], timeout: 5)
        XCTAssertEqual(hittable, .completed, "after the sheet is opened, Day 2 can be tapped (the sheet is \(handle.value ?? "?"))")
        day2.tap()
        XCTAssertTrue(day2.isSelected, "Day 2 is the one picked")
        expectPins(map, "3 places along the route", "a day still has its pins")
    }

    private func expectPins(_ map: XCUIElement, _ spoken: String, _ why: String, file: StaticString = #filePath, line: UInt = #line) {
        let ok = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "value == %@", spoken), object: map)], timeout: 15) == .completed
        XCTAssertTrue(ok, "\(why): expected \(spoken), the map says \(String(describing: map.value))", file: file, line: line)
    }

    private func expectNoPins(_ map: XCUIElement, _ why: String, file: StaticString = #filePath, line: UInt = #line) {
        let empty = NSPredicate { obj, _ in ((obj as? XCUIElement)?.value as? String ?? "") == "" }
        let ok = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: empty, object: map)], timeout: 15) == .completed
        XCTAssertTrue(ok, "\(why): the map says \(String(describing: map.value))", file: file, line: line)
    }
}
