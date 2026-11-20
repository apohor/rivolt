import XCTest

/// With design_preview now_parked_graph (and ios_now_v2, ios_now_tidy, now_slot): a parked truck answered from generated fixtures (`-uitest-parked`,
/// Rivolt/App/UITestFixtures.swift, the same numbers as the web's e2e stub) shows the last 24 hours of the battery, the sleeps and the three figures.
final class NowParkedGraphUITests: XCTestCase {
    private func launch(_ parkedCase: String = "overnight", dark: Bool = false, xxxLarge: Bool = false, metric: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["RIVOLT_UITEST_PARKED_CASE"] = parkedCase
        // Fixture times sit against a five-minute mark just behind the real time; the web shots are taken at the same instant (STUB_NOW).
        let mark = Date(timeIntervalSince1970: (Date().timeIntervalSince1970 / 300).rounded(.down) * 300)
        let stamp = ISO8601DateFormatter().string(from: mark)
        app.launchEnvironment["RIVOLT_UITEST_PARKED_NOW"] = stamp
        try? FileManager.default.createDirectory(atPath: "/tmp/rivolt-ui-shots", withIntermediateDirectories: true)
        try? stamp.write(toFile: "/tmp/rivolt-ui-shots/parked-now-\(parkedCase)", atomically: true, encoding: .utf8)
        if dark { app.launchArguments += ["-rivolt.appearance", "dark"] }
        if xxxLarge { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryXXXL"] }
        app.launchEnvironment["RIVOLT_UITEST_UNITS"] = metric ? "km" : "mi"
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += ["-uitest-now-v2", "-uitest-parked"]
        app.launch()
        return app
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        try? FileManager.default.createDirectory(atPath: "/tmp/rivolt-ui-shots", withIntermediateDirectories: true)
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/rivolt-ui-shots/\(name).png"))
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    func testAnOvernightParkDrawsTheGraphAndTheFigures() {
        let app = launch()
        let graph = element(app, "now-parked-graph")
        let appeared = graph.waitForExistence(timeout: 30)
        if !appeared { shot(app, "now-parked-graph-missing") }
        XCTAssertTrue(appeared, "the graph appears once the series is read")
        XCTAssertTrue(graph.label.contains("Battery while parked"), "VoiceOver says what it is")
        XCTAssertTrue(graph.label.contains("5 sleeps, 4 wake-ups"), "and the counts")
        XCTAssertTrue(element(app, "now-parked-change").exists, "the header's change")
        XCTAssertTrue(element(app, "now-parked-tile-idle").exists, "the idle-loss tile")
        XCTAssertTrue(element(app, "now-parked-projection").exists, "the projection")
        // The card is one VoiceOver element, so the line is found in what it says.
        let card = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS 'At Home'")).firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 10), "the place line stands where the map was, and the card says it")
        shot(app, "now-parked-graph-top")
    }

    /// Only the Live button opens Live: a tap on the card's body, its graph, its figures or the place line stays on Now.
    func testOnlyTheLiveButtonOpensLive() {
        let app = launch()
        let graph = element(app, "now-parked-graph")
        XCTAssertTrue(graph.waitForExistence(timeout: 30))
        let live = element(app, "now-live-screen")
        graph.tap()
        element(app, "now-parked-tile-idle").tap()
        let place = element(app, "now-parked-place")
        if place.exists { place.tap() }
        let chip = element(app, "now-chip")
        if chip.exists { app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: chip.frame.midX, dy: chip.frame.maxY + 40)).tap() }
        XCTAssertFalse(live.waitForExistence(timeout: 2), "nothing but the Live button opens Live")
        XCTAssertTrue(element(app, "now-parked-graph").exists, "still on Now")
        // The Live button is its own element with its own name; the card's text does not hold it.
        let button = app.buttons["now-live-link"].firstMatch
        XCTAssertTrue(button.exists)
        XCTAssertEqual(button.label, "Live")
        button.tap()
        XCTAssertTrue(live.waitForExistence(timeout: 10), "the button opens Live")
    }

    func testTooLittleLeavesTheCardAsItWas() {
        let app = launch("minutes")
        XCTAssertTrue(element(app, "now-slot-parked").waitForExistence(timeout: 30), "the parked slot still shows")
        XCTAssertFalse(element(app, "now-parked-graph").exists, "no graph for a truck parked a few minutes")
        XCTAssertFalse(element(app, "now-parked-place").exists, "with no place line")
    }

    /// The parity shots: the same states as the web's matrix (web/e2e/now-parked-graph.pw.ts), so the two cards can be set side by side.
    /// Not a gate beyond "the graph appears"; the shots are looked at by hand and filed under _shared/parked-graph/ios.
    func testParityShots() throws {
        guard ProcessInfo.processInfo.environment["RIVOLT_PARITY_SHOTS"] != nil || FileManager.default.fileExists(atPath: "/tmp/rivolt-ui-shots/.parity") else { throw XCTSkip("shots on request") }
        let combos: [(String, Bool, Bool, Bool)] = [
            ("overnight", true, false, false), ("overnight", false, false, false), ("overnight", true, false, true), ("overnight", true, true, false),
            ("charged", true, false, false), ("asleep", true, false, false), ("never_slept", true, false, false), ("never_slept", false, true, false),
        ]
        for (name, dark, big, metric) in combos {
            let app = launch(name, dark: dark, xxxLarge: big, metric: metric)
            XCTAssertTrue(element(app, "now-parked-graph").waitForExistence(timeout: 30), "\(name) graph")
            let tag = "\(name)-\(dark ? "dark" : "light")-\(big ? "xxxl" : "std")-\(metric ? "km" : "mi")"
            shot(app, "parity-\(tag)-top")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8)).press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)))
            shot(app, "parity-\(tag)-middle")
            app.terminate()
        }
    }
}
