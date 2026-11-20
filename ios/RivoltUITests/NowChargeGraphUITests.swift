import XCTest

/// With design_preview now_charge_graph (and ios_now_v2, ios_now_tidy, now_slot): a charge that began 40 minutes ago, answered from generated fixtures
/// (`-uitest-charging`, Rivolt/App/UITestFixtures.swift), shows the battery graph, the session rows and the pack temperature, as the web's card does.
final class NowChargeGraphUITests: XCTestCase {
    private func launch(_ chargeCase: String = "ac", dark: Bool = false, xxxLarge: Bool = false, metric: Bool = true) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["RIVOLT_UITEST_CHARGE_CASE"] = chargeCase
        if dark { app.launchArguments += ["-rivolt.appearance", "dark"] }
        if xxxLarge { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryXXXL"] }
        // The parity shots place the history at the instant the web shots were taken at (STUB_NOW), so both show the same clock times.
        if let pinned = try? String(contentsOfFile: "/tmp/rivolt-ui-shots/.parity-now", encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines), !pinned.isEmpty {
            app.launchEnvironment["RIVOLT_UITEST_CHARGE_NOW"] = pinned
        }
        app.launchEnvironment["RIVOLT_UITEST_UNITS"] = metric ? "km" : "mi"
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += ["-uitest-now-v2", "-uitest-charging"]
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

    /// A truck that is charging has no picture on the Live screen, so the plug badge and glow (design_preview plug_anim) have nothing to sit on and are not drawn.
    func testLiveWhileChargingHasNoPictureAndNoPlugBadge() {
        let app = launch()
        XCTAssertTrue(element(app, "now-charge-graph").waitForExistence(timeout: 30))
        let live = app.buttons["now-live-link"].firstMatch
        XCTAssertTrue(live.waitForExistence(timeout: 10))
        live.tap()
        XCTAssertTrue(element(app, "now-live-screen").waitForExistence(timeout: 10), "Live opens")
        shot(app, "live-charging-no-picture")
        XCTAssertTrue(app.staticTexts["Streaming vehicle state from Rivian"].exists, "the truck is streaming, so this is the charging Live screen")
        XCTAssertFalse(app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH 'Charging, ' OR label BEGINSWITH 'Plugged in, not'")).firstMatch.exists, "no plug badge on a missing picture")
    }

    func testTheChargingCardShowsTheGraphAndTheQuietRows() {
        let app = launch()
        let graph = element(app, "now-charge-graph")
        XCTAssertTrue(graph.waitForExistence(timeout: 30), "the graph appears once the stored samples are read")
        XCTAssertTrue(graph.label.contains("percent"), "VoiceOver says the battery's start and now")
        XCTAssertTrue(element(app, "now-charge-graph-change").exists, "the header's change since plug-in")
        XCTAssertTrue(element(app, "now-charge-graph-estimate").exists, "the dotted way on to the target is in the legend")
        // The card reads as one element to VoiceOver; its words hold the session rows, and no "Plugged in" row.
        let card = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS 'Elapsed'")).firstMatch
        XCTAssertTrue(card.exists, "the session rows")
        XCTAssertFalse(card.label.contains("Plugged in"), "plug-in time is the graph's label, not a row")
        XCTAssertTrue(card.label.contains("Cost so far"), "the cost so far row")
        XCTAssertFalse(card.label.contains("≈"), "no approximately sign: \(card.label)")
        XCTAssertTrue(card.label.contains("estimated"), "an estimated figure is spoken as estimated: \(card.label)")
        // The pack's temperature is the graph's third line: the graph's words say it, and no hero figure or row carries it.
        XCTAssertTrue(graph.label.contains("pack temperature"), "the graph says the pack now: \(graph.label)")
        XCTAssertFalse(card.label.contains(" pack."), "no hero pack figure: \(card.label)")
        XCTAssertFalse(card.label.contains("Pack temperature"), "no pack temperature row: \(card.label)")
        shot(app, "now-charge-graph-top")
        let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8))
        from.press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)))
        shot(app, "now-charge-graph-middle")
        app.swipeUp()
        shot(app, "now-charge-graph-scrolled")
    }

    /// The parity shots: the same states as the web's e2e matrix (web/e2e/now-charge-graph.pw.ts), so the two cards can be set side by side.
    /// Not a gate beyond "the graph appears"; the shots are looked at by hand and filed under _shared/charge-graph/ios.
    func testParityShots() throws {
        guard ProcessInfo.processInfo.environment["RIVOLT_PARITY_SHOTS"] != nil || FileManager.default.fileExists(atPath: "/tmp/rivolt-ui-shots/.parity") else { throw XCTSkip("shots on request") }
        let combos: [(String, Bool, Bool, Bool)] = [
            ("dc", true, false, false), ("dc", false, false, false), ("dc", true, false, true), ("dc", true, true, false),
            ("slow", true, false, false), ("slow", false, false, false), ("slow", true, true, false), ("slow", true, false, true),
            ("ac", true, false, false), ("ac", false, false, true),
        ]
        for (name, dark, big, metric) in combos {
            let app = launch(name, dark: dark, xxxLarge: big, metric: metric)
            XCTAssertTrue(element(app, "now-charge-graph").waitForExistence(timeout: 30), "\(name) graph")
            let tag = "\(name)-\(dark ? "dark" : "light")-\(big ? "xxxl" : "std")-\(metric ? "km" : "mi")"
            shot(app, "parity-\(tag)-top")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8)).press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.42)))
            shot(app, "parity-\(tag)-graph")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8)).press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)))
            shot(app, "parity-\(tag)-middle")
            app.terminate()
        }
    }
}
