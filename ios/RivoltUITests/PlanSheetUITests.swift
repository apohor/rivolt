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
        // A tap that lands while the list is still settling opens nothing (a CI run failed on "the sheet is there" with the trip still listed):
        // tap once more if the sheet is not up after 8 s and the trip is still on screen.
        if !grabber.waitForExistence(timeout: 8), trip.exists { trip.tap() }
        XCTAssertTrue(grabber.waitForExistence(timeout: 20), "the sheet is there")
        return grabber
    }

    /// 20 s like the other waits here: reading the grabber's value waits for the app to be idle first, and the planner's map keeps a slow CI simulator busy
    /// past 5 s (a run failed with "the sheet is half, not half": the value was right by the time the message read it). A pass returns at once.
    private func expect(_ grabber: XCUIElement, _ value: String, _ why: String, file: StaticString = #filePath, line: UInt = #line) {
        let p = NSPredicate(format: "value == %@", value)
        let ok = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: p, object: grabber)], timeout: 20) == .completed
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
        // The start is read from the grabber's frame each time and the drag is held 0.3 s at the end so the sheet settles on the detent; a drag that
        // a loaded CI simulator dropped (the finger down but no move) is made once more, the sheet being still at the peek.
        let top = app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25))
        for attempt in 1...2 where grabber.value as? String != "full" {
            grabber.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
                .press(forDuration: 0.1, thenDragTo: top, withVelocity: .default, thenHoldForDuration: 0.3)
            if attempt == 1 {
                _ = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "value == %@", "full"), object: grabber)], timeout: 8)
            }
        }
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
        XCTAssertTrue(day2.becomesSelected(), "the day chip took the tap")
        expect(grabber, "half", "and the sheet stayed where it was")
        app.buttons["Edit"].firstMatch.tap()
        // The form is the proof the tap landed: wait for it, then for the sheet to leave. A slow runner took 2 s to show the form and kept the sheet in
        // the tree past the 5 s this used to allow (the failed run's video shows the form on screen), so both waits are 20 s like the other openers.
        XCTAssertTrue(app.buttons["Back to the map"].firstMatch.waitForExistence(timeout: 20), "Edit took the tap and opened the trip's form")
        let gone = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: grabber)], timeout: 20)
        XCTAssertEqual(gone, .completed, "and the sheet left the map screen")
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

    /// At the largest sizes the trip's name is cut to two lines above the full detent and wraps in whole at it (#2136). The runner is not at AX3
    /// itself, so the check is the title's own height at half against full: with `lineLimit(2)` at both, the two are equal.
    func testAtAX3TheTripTitleIsTallerAtFullThanAtHalf() throws {
        let app = launch(["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXL"])
        let grabber = open("Two days to Spokane", in: app)
        grabber.tap()
        expect(grabber, "half", "the grabber raises it to half")
        let title = app.descendants(matching: .any)["plan-sheet-title"].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 10), "the title is in the header")
        let atHalf = title.frame.height
        grabber.tap()
        expect(grabber, "full", "the grabber raises it to full")
        let atFull = title.frame.height
        XCTAssertGreaterThan(atFull, atHalf + 20, "the name of the trip shows more lines at full (\(atFull) pt) than at half (\(atHalf) pt)")
    }

    /// Brings the elevation chart into view by opening the sheet and scrolling its details, and returns it.
    private func chart(in app: XCUIApplication, grabber: XCUIElement) -> XCUIElement {
        let chart = app.descendants(matching: .any)["plan-elevation-chart"].firstMatch
        if grabber.value as? String != "full" { grabber.tap(); if grabber.value as? String != "full" { grabber.tap() } }
        let window = app.windows.firstMatch
        // In view means below the pinned header and above the tab bar, not just on screen. Drags stay inside the details (under the header).
        for _ in 0..<12 {
            if grabber.value as? String != "full" { grabber.tap() }
            // The chart is one row until opened (the choice is kept, so it may already be open).
            let toggle = app.buttons["plan-elevation-toggle"].firstMatch
            if !chart.exists, toggle.exists, toggle.isHittable { toggle.tap() }
            let high = chart.exists && chart.frame.minY < 420
            if chart.exists && !high && chart.frame.maxY <= window.frame.maxY - 120 { break }
            let from = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: high ? 0.55 : 0.85))
            let to = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: high ? 0.7 : 0.6))
            from.press(forDuration: 0.1, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.2)
        }
        Thread.sleep(forTimeInterval: 1)   // let the scroll settle before anything is aimed at it
        XCTAssertTrue(chart.exists && chart.isHittable, "the plan has an elevation chart in view")
        return chart
    }

    /// With the Now redesign's key too, the Plan tab's top bar has the sun/moon switch like every other tab's.
    func testThePlanTopBarHasTheThemeSwitchWithBothKeys() throws {
        let app = launch(["-uitest-ios-elevation", "-uitest-now-v2"])
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20))
        plan.tap()
        XCTAssertTrue(app.buttons["theme-switch"].firstMatch.waitForExistence(timeout: 10), "the Plan top bar has the switch")
    }

    /// Without the design_preview key ios_elevation the plan has no chart (prod's screen is unchanged).
    func testWithoutTheFlagThePlanHasNoElevationChart() throws {
        let app = launch()
        let grabber = open("Seattle to Spokane", in: app)
        if grabber.value as? String != "full" { grabber.tap() }
        XCTAssertTrue(app.staticTexts.firstMatch.waitForExistence(timeout: 10))
        for _ in 0..<3 { app.windows.firstMatch.swipeUp() }
        XCTAssertFalse(app.descendants(matching: .any)["plan-elevation-chart"].firstMatch.exists)
    }

    /// A tap on the elevation chart sets its cursor (the value says where) and does not move the sheet.
    func testTappingTheElevationChartSetsItsCursorAndLeavesTheSheet() throws {
        let app = launch(["-uitest-ios-elevation"])
        let grabber = open("Seattle to Spokane", in: app)
        let chart = chart(in: app, grabber: grabber)
        let sheet = grabber.value as? String ?? "?"
        chart.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.5)).tap()
        let first = chart.value as? String ?? ""
        XCTAssertTrue(first.range(of: #"^[\d,]+ (mi|km) · [\d,]+ (ft|m)$"#, options: .regularExpression) != nil, "the cursor has a place and a height: \(first)")
        chart.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: 0.5)).tap()
        XCTAssertNotEqual(chart.value as? String ?? "", first, "a second tap moved the cursor")
        expect(grabber, sheet, "a tap on the chart leaves the sheet")
    }

    /// The terrain credit is on the plan once, with the elevation row collapsed and with it open.
    func testTheTerrainCreditIsOnThePlanOnceCollapsedAndOpen() throws {
        let app = launch(["-uitest-ios-elevation"])
        let grabber = open("Seattle to Spokane", in: app)
        let credit = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH 'Terrain: USGS'"))
        if grabber.value as? String != "full" { grabber.tap() }
        let toggle = app.buttons["plan-elevation-toggle"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 10))
        if toggle.value as? String == "Chart shown" { toggle.tap() }
        XCTAssertEqual(toggle.value as? String, "Chart hidden", "the chart starts collapsed")
        XCTAssertEqual(credit.count, 1, "collapsed: the climb figures show with the credit, once")
        _ = chart(in: app, grabber: grabber)
        XCTAssertEqual(credit.count, 1, "open: still one credit, not one under the chart and one under the stops")
    }

    /// A vertical swipe that starts on the chart scrolls the details (a row below the chart moves, the sheet stays) and leaves the cursor.
    func testAVerticalSwipeStartingOnTheChartScrollsTheDetailsAndLeavesTheCursor() throws {
        let app = launch(["-uitest-ios-elevation"])
        let grabber = open("Seattle to Spokane", in: app)
        let chart = chart(in: app, grabber: grabber)
        chart.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.5)).tap()
        let cursor = chart.value as? String ?? ""
        let sheet = grabber.value as? String ?? "?"
        let grabberTop = grabber.frame.minY
        let note = app.staticTexts["Elevation shown for context; Rivian's estimate decides the energy."].firstMatch
        XCTAssertTrue(note.exists, "a row below the chart")
        let noteTop = note.frame.minY
        let start = chart.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        start.press(forDuration: 0.05, thenDragTo: start.withOffset(CGVector(dx: 0, dy: -160)), withVelocity: .slow, thenHoldForDuration: 0.1)   // up: the details have more below the chart
        XCTAssertEqual(chart.value as? String ?? "", cursor, "a vertical swipe leaves the cursor")
        XCTAssertNotEqual(note.frame.minY, noteTop, "the swipe scrolled the details")
        XCTAssertEqual(grabber.frame.minY, grabberTop, accuracy: 1, "the sheet stayed put")
        expect(grabber, sheet, "the sheet keeps its detent")
    }

    /// At accessibility text the chart is still whole: on the screen's width, with its words.
    func testTheElevationChartFitsAtAX3() throws {
        let app = launch(["-uitest-ios-elevation", "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXL"])
        let grabber = open("Seattle to Spokane", in: app)
        let chart = chart(in: app, grabber: grabber)
        let window = app.windows.firstMatch.frame
        XCTAssertGreaterThanOrEqual(chart.frame.minX, window.minX)
        XCTAssertLessThanOrEqual(chart.frame.maxX, window.maxX)
        XCTAssertGreaterThanOrEqual(chart.frame.height, 100, "the plot keeps its height")
        XCTAssertTrue(app.staticTexts["Elevation shown for context; Rivian's estimate decides the energy."].firstMatch.exists, "the context line is there")
    }

    /// At the largest accessibility sizes on a small phone the sheet's top (title, summary, Edit, the day chips) fills the screen: a person opens the details and scrolls,
    /// and every day chip has to come wholly above the tab bar and take a tap (QA found Day 2 never did on the iPhone SE).
    func testEveryDayChipIsReachableAtAX3() throws { try everyDayChipIsReachable(category: "UICTContentSizeCategoryAccessibilityXL") }

    /// The same at AX5, the largest size.
    func testEveryDayChipIsReachableAtAX5() throws { try everyDayChipIsReachable(category: "UICTContentSizeCategoryAccessibilityXXXL") }

    private func everyDayChipIsReachable(category: String) throws {
        let app = launch(["-UIPreferredContentSizeCategoryName", category])
        let grabber = open("Two days, apart", in: app)
        grabber.tap()
        if grabber.value as? String != "full" { grabber.tap() }
        expect(grabber, "full", "the details are open")
        let window = app.windows.firstMatch
        let tabTop = app.buttons["Plan"].firstMatch.frame.minY
        for chip in ["Whole trip", "Day 1", "Day 2"] {
            let button = app.buttons[chip].firstMatch
            var seen = ""
            var reached = false
            for step in 0..<8 {
                let f = button.exists ? button.frame : .zero
                seen += "step \(step): exists=\(button.exists) frame=\(f) tabTop=\(tabTop)\n"
                if button.exists, button.isHittable, f.maxY <= tabTop { reached = true; break }
                // A person's drag up from the lower part of the screen, which is the details' to scroll (not the title's: that would close the sheet).
                let from = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
                let by: CGFloat = -110
                from.press(forDuration: 0.1, thenDragTo: from.withOffset(CGVector(dx: 0, dy: by)), withVelocity: .slow, thenHoldForDuration: 0.2)
            }
            XCTAssertTrue(reached, "\(chip) never wholly above the tab bar and tappable:\n\(seen)")
        }
        app.buttons["Day 2"].firstMatch.tap()
        XCTAssertTrue(app.buttons["Day 2"].firstMatch.isSelected, "a tap on Day 2 picks it")
    }
}
