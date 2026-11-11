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

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = Self.fixtureDirectories
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
}
