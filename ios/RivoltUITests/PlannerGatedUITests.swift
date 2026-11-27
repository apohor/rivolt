import XCTest

/// A new free account on the Plan tab: planning is Pro, the planner answers 402, and the screen says so in the app's own words (no price, no link).
/// Every call is answered from fixtures (Rivolt/App/UITestFixtures.swift, `-uitest-planner-gated`).
final class PlannerGatedUITests: XCTestCase {
    func testGatedPlanShowsTheTierFact() throws {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += ["-uitest-planner-gated"]
        app.launch()
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20), "the Plan tab is there")
        plan.tap()
        // The fixture truck has no position, so pick a starting place too.
        func pick(_ row: String, _ place: String) {
            let r = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", row)).firstMatch
            XCTAssertTrue(r.waitForExistence(timeout: 20), "\(row) row")
            r.tap()
            let f = app.textFields["A city, or a street and a city"].firstMatch
            XCTAssertTrue(f.waitForExistence(timeout: 10))
            f.tap(); f.typeText("Spokane")
            let m = app.buttons.matching(NSPredicate(format: "label BEGINSWITH '\(place)'")).firstMatch
            XCTAssertTrue(m.waitForExistence(timeout: 20), "the place is listed")
            m.tap()
            if !f.waitForNonExistence(timeout: 4), m.exists { m.tap() }  // a tap while the list settles can miss
            XCTAssertTrue(f.waitForNonExistence(timeout: 10), "the picker closes")
        }
        pick("Where the truck is", "Example Origin")
        pick("Choose a place", "Example Destination")
        let go = app.buttons["Plan the trip"].firstMatch
        XCTAssertTrue(go.waitForExistence(timeout: 10))
        go.tap()
        let said = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "part of the Pro tier")).firstMatch
        _ = said.waitForExistence(timeout: 20)
        let shot = app.screenshot()
        let a = XCTAttachment(screenshot: shot); a.name = "plan-gated"; a.lifetime = .keepAlways; add(a)
        try? FileManager.default.createDirectory(atPath: "/tmp/claude-501/shots", withIntermediateDirectories: true)
        try? shot.pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/claude-501/shots/plan-gated.png"))
        XCTAssertTrue(said.exists, "the 402 reads as a tier fact")
    }
}
