import XCTest

/// The planner's sheet, driven like a person would, with every call answered from fixtures (Rivolt/App/UITestFixtures.swift): a tap on the peek opens
/// the details and the Map button closes them again (five times over), an upright drag moves the sheet while a sideways one does not, and the buttons
/// in its header (the day chips, Edit) still take their taps. The grabber's accessibility value is the sheet's height: peek, half or full.
final class PlanSheetUITests: XCTestCase {
    private static var fixtureDirectories: String {
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        return [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
    }

    private func launch(_ arguments: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = Self.fixtureDirectories
        app.launchArguments += arguments
        app.launch()
        return app
    }

    /// Opens the saved trip called `name` on the Plan tab and returns the sheet's grabber.
    private func open(_ name: String, in app: XCUIApplication) -> XCUIElement {
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20), "the Plan tab is there")
        plan.tap()
        let trip = app.buttons[name].firstMatch
        XCTAssertTrue(trip.waitForExistence(timeout: 20), "the saved trip \(name) is listed")
        trip.tap()
        let grabber = app.descendants(matching: .any)["plan-sheet-grabber"].firstMatch
        XCTAssertTrue(grabber.waitForExistence(timeout: 20), "the sheet is there")
        return grabber
    }

    private func expect(_ grabber: XCUIElement, _ value: String, _ why: String, file: StaticString = #filePath, line: UInt = #line) {
        let p = NSPredicate(format: "value == %@", value)
        let ok = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: p, object: grabber)], timeout: 5) == .completed
        XCTAssertTrue(ok, "\(why): the sheet is \(grabber.value ?? "?"), not \(value)", file: file, line: line)
    }

    func testMapToDetailsAndBackFiveTimes() throws {
        let app = launch()
        let grabber = open("Seattle to Spokane", in: app)
        expect(grabber, "half", "it opens at half")
        let map = app.buttons["plan-sheet-map"].firstMatch
        let title = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "→")).firstMatch
        for round in 1...5 {
            map.tap()
            expect(grabber, "peek", "round \(round): Map shows the map")
            XCTAssertFalse(map.exists, "round \(round): no Map button at the peek")
            title.tap()
            expect(grabber, "full", "round \(round): a tap on the peek opens the details")
        }
        map.tap()
        expect(grabber, "peek", "and back to the map")
        // The grabber steps through the three.
        grabber.tap()
        expect(grabber, "half", "grabber from the peek")
        grabber.tap()
        expect(grabber, "full", "grabber from half")
        grabber.tap()
        expect(grabber, "peek", "grabber from full")
    }

    func testAnUprightDragMovesTheSheetAndASidewaysOneDoesNot() throws {
        let app = launch()
        let grabber = open("Seattle to Spokane", in: app)
        expect(grabber, "half", "it opens at half")
        let title = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "→")).firstMatch
        let from = title.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 160, dy: 0)))
        expect(grabber, "half", "a sideways drag leaves it")
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: 260)))
        expect(grabber, "peek", "a drag down closes it to the peek")
    }

    func testALongDragUpOnTheGrabberOpensTheDetails() throws {
        let app = launch()
        let grabber = open("Seattle to Spokane", in: app)
        app.buttons["plan-sheet-map"].firstMatch.tap()
        expect(grabber, "peek", "Map shows the map")
        // The grabber rides up under the finger: letting go on it is the end of a drag, not a tap that steps the sheet on.
        grabber.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
            .press(forDuration: 0.1, thenDragTo: app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)))
        expect(grabber, "full", "a drag up past half opens the details")
    }

    func testAtFullADragDownFromTheTopOfScrolledDetailsLeavesFull() throws {
        let app = launch()
        let grabber = open("Two days to Spokane", in: app)
        let window = app.windows.firstMatch
        for round in 1...2 {
            if grabber.value as? String != "full" { grabber.tap(); if grabber.value as? String != "full" { grabber.tap() } }
            expect(grabber, "full", "round \(round): the details are open")
            // Up and back down by the same slow drag: the details scroll and end at their top, and the sheet stays.
            let low = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.75))
            let high = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.55))
            low.press(forDuration: 0.1, thenDragTo: high, withVelocity: .slow, thenHoldForDuration: 0.3)
            expect(grabber, "full", "round \(round): scrolling the details leaves the sheet")
            high.press(forDuration: 0.1, thenDragTo: low, withVelocity: .slow, thenHoldForDuration: 0.3)
            expect(grabber, "full", "round \(round): scrolling back to the top leaves the sheet")
            let mid = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45))
            mid.press(forDuration: 0.1, thenDragTo: mid.withOffset(CGVector(dx: 0, dy: 320)))
            let left = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "value != %@", "full"), object: grabber)], timeout: 5)
            XCTAssertEqual(left, .completed, "round \(round): a drag down from the top of the details closes the sheet down")
        }
    }

    func testASidewaysSwipeOnTheDayChipsLeavesTheNextDragWorking() throws {
        let app = launch()
        let grabber = open("Two days to Spokane", in: app)
        expect(grabber, "half", "it opens at half")
        let day2 = app.buttons["Day 2"].firstMatch
        XCTAssertTrue(day2.waitForExistence(timeout: 10))
        let chip = day2.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        chip.press(forDuration: 0.05, thenDragTo: chip.withOffset(CGVector(dx: -160, dy: 0)))
        expect(grabber, "half", "a sideways swipe on the chips leaves the sheet")
        let title = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "→")).firstMatch
        title.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
            .press(forDuration: 0.1, thenDragTo: app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)))
        expect(grabber, "full", "and the drag up on the header after it moves the sheet")
    }

    func testTheHeadersButtonsStillTakeTheirTaps() throws {
        let app = launch()
        let grabber = open("Two days to Spokane", in: app)
        expect(grabber, "half", "it opens at half")
        let day2 = app.buttons["Day 2"].firstMatch
        XCTAssertTrue(day2.waitForExistence(timeout: 10))
        day2.tap()
        XCTAssertTrue(day2.isSelected, "the day chip took the tap")
        expect(grabber, "half", "and the sheet stayed where it was")
        app.buttons["Edit"].firstMatch.tap()
        let gone = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: grabber)], timeout: 5)
        XCTAssertEqual(gone, .completed, "Edit took the tap and left the map for the trip's form")
    }

    /// #1886: at AX3 half Edit and the day chips sat under the tab bar: the header's height never reached the screen (it was sent back as 0), so half
    /// fell to its floor. This drives the real screen, so it covers the wiring as well as SheetLayout's sums.
    func testAtAX3HalfEditAndTheDayChipsSitWholeAboveTheTabBar() throws {
        let app = launch(["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXL"])
        let grabber = open("Two days to Spokane", in: app)
        expect(grabber, "peek", "at accessibility sizes it opens at the peek")
        grabber.tap()
        expect(grabber, "half", "the grabber raises it to half")
        let tabTop = app.buttons["Activity"].firstMatch.frame.minY   // the custom tab bar (ThemeTabBar): its buttons fill it
        for name in ["Edit", "Whole trip", "Day 1", "Day 2"] {
            let button = app.buttons[name].firstMatch
            XCTAssertTrue(button.waitForExistence(timeout: 10), "\(name) is there")
            XCTAssertLessThanOrEqual(button.frame.maxY, tabTop + 0.5, "\(name) ends above the tab bar (\(button.frame), tab bar at \(tabTop))")
            XCTAssertTrue(button.isHittable, "\(name) takes a tap")
        }
    }

    /// Brings the elevation chart into view by opening the sheet and scrolling its details, and returns it.
    private func chart(in app: XCUIApplication, grabber: XCUIElement) -> XCUIElement {
        let chart = app.descendants(matching: .any)["plan-elevation-chart"].firstMatch
        if grabber.value as? String != "full" { grabber.tap(); if grabber.value as? String != "full" { grabber.tap() } }
        let window = app.windows.firstMatch
        for _ in 0..<8 where !chart.exists || !chart.isHittable || chart.frame.maxY > window.frame.maxY - 120 {
            let low = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8))
            low.press(forDuration: 0.1, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)), withVelocity: .slow, thenHoldForDuration: 0.2)
        }
        XCTAssertTrue(chart.exists && chart.isHittable, "the plan has an elevation chart in view")
        return chart
    }

    /// A drag along the elevation chart moves its cursor (the value says where) and does not move the sheet.
    func testDraggingAlongTheElevationChartMovesItsCursorAndLeavesTheSheet() throws {
        let app = launch()
        let grabber = open("Seattle to Spokane", in: app)
        let chart = chart(in: app, grabber: grabber)
        let sheet = grabber.value as? String ?? "?"
        // The scroll that brought the chart into view may have touched it, so read the cursor where a first touch puts it, then drag on.
        let from = chart.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.5))
        from.tap()
        let before = chart.value as? String ?? ""
        from.press(forDuration: 0.05, thenDragTo: chart.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: 0.5)))
        let value = chart.value as? String ?? ""
        XCTAssertNotEqual(value, before, "the drag moved the cursor")
        XCTAssertTrue(value.range(of: #"^[\d,]+ (mi|km) · [\d,]+ (ft|m)$"#, options: .regularExpression) != nil, "the cursor has a place and a height: \(value)")
        expect(grabber, sheet, "a sideways drag on the chart leaves the sheet")
    }

    /// A vertical swipe that starts on the chart scrolls the details and leaves the cursor where it was.
    func testAVerticalSwipeStartingOnTheChartScrollsTheDetailsAndLeavesTheCursor() throws {
        let app = launch()
        let grabber = open("Seattle to Spokane", in: app)
        let chart = chart(in: app, grabber: grabber)
        chart.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.5)).tap()
        let cursor = chart.value as? String ?? ""
        let top = chart.frame.minY
        let start = chart.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        start.press(forDuration: 0.05, thenDragTo: start.withOffset(CGVector(dx: 0, dy: 160)), withVelocity: .slow, thenHoldForDuration: 0.1)
        XCTAssertEqual(chart.value as? String ?? "", cursor, "a vertical swipe leaves the cursor")
        XCTAssertNotEqual(chart.frame.minY, top, "the swipe scrolled the details")
    }

    /// At accessibility text the chart is still whole: on the screen's width, with its words.
    func testTheElevationChartFitsAtAX3() throws {
        let app = launch(["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXL"])
        let grabber = open("Seattle to Spokane", in: app)
        let chart = chart(in: app, grabber: grabber)
        let window = app.windows.firstMatch.frame
        XCTAssertGreaterThanOrEqual(chart.frame.minX, window.minX)
        XCTAssertLessThanOrEqual(chart.frame.maxX, window.maxX)
        XCTAssertGreaterThanOrEqual(chart.frame.height, 100, "the plot keeps its height")
        XCTAssertTrue(app.staticTexts["Elevation shown for context; Rivian's estimate decides the energy."].firstMatch.exists, "the context line is there")
    }
}
