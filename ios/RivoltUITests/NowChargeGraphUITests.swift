import XCTest

/// With design_preview now_charge_graph (and ios_now_v2, ios_now_tidy, now_slot): a charge that began 40 minutes ago, answered from generated fixtures
/// (`-uitest-charging`, Rivolt/App/UITestFixtures.swift), shows the battery graph, the session rows and the pack temperature, as the web's card does.
final class NowChargeGraphUITests: XCTestCase {
    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
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
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH 'Pack '")).firstMatch.exists, "the pack temperature, from the newest stored sample")
        shot(app, "now-charge-graph-top")
        let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8))
        from.press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)))
        shot(app, "now-charge-graph-middle")
        app.swipeUp()
        shot(app, "now-charge-graph-scrolled")
    }
}
