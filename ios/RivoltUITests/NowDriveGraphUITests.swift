import XCTest

/// With design_preview now_drive_graph (and ios_now_v2, ios_now_tidy, now_slot, live_destination): a drive in progress, answered from generated fixtures
/// (`-uitest-driving`, Rivolt/App/UITestFixtures.swift), shows the speed and battery graph, the destination line and the figures, as the web's card does.
final class NowDriveGraphUITests: XCTestCase {
    private func launch(_ driveCase: String = "middle", dest: Bool = true, dark: Bool = false, metric: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchEnvironment["RIVOLT_UITEST_DRIVE_CASE"] = driveCase
        if !dest { app.launchEnvironment["RIVOLT_UITEST_DRIVE_DEST"] = "none" }
        app.launchArguments += ["-rivolt.appearance", dark ? "dark" : "light"]
        // The parity shots place the history at the instant the web shots were taken at (STUB_NOW), so both show the same clock times.
        if let pinned = try? String(contentsOfFile: "/tmp/rivolt-ui-shots/.parity-now", encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines), !pinned.isEmpty {
            app.launchEnvironment["RIVOLT_UITEST_CHARGE_NOW"] = pinned
        }
        app.launchEnvironment["RIVOLT_UITEST_UNITS"] = metric ? "km" : "mi"
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += ["-uitest-now-v2", "-uitest-driving"]
        app.launch()
        assertRendered(app, dark: dark)
        return app
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let screenshot = app.screenshot()  // one capture: a second request is a second chance to time out on a loaded runner
        let a = XCTAttachment(screenshot: screenshot)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        try? FileManager.default.createDirectory(atPath: "/tmp/rivolt-ui-shots", withIntermediateDirectories: true)
        try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/rivolt-ui-shots/\(name).png"))
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    func testTheDrivingCardShowsTheGraphAndTheDestination() {
        let app = launch()
        let graph = element(app, "now-drive-graph")
        XCTAssertTrue(graph.waitForExistence(timeout: 30), "the graph appears once the stored samples are read")
        XCTAssertTrue(graph.label.contains("mph"), "VoiceOver says the speed: \(graph.label)")
        XCTAssertTrue(element(app, "now-drive-change").exists, "the header's change since the start")
        XCTAssertTrue(element(app, "now-drive-dest").waitForExistence(timeout: 10), "the destination line")
        XCTAssertTrue(graph.label.contains("12 mi to Test Depot"), "the line is spoken with the graph: \(graph.label)")
        XCTAssertTrue(element(app, "now-drive-top").exists, "the top speed label")
        XCTAssertFalse(element(app, "live-destination").exists, "the destination block would say it twice")
        shot(app, "now-drive-graph-top")
    }

    func testAFreshDriveHasTheFrameAndAShortState() {
        let app = launch("fresh")
        XCTAssertTrue(element(app, "now-slot-figures").waitForExistence(timeout: 30))
        XCTAssertTrue(element(app, "now-drive-graph").waitForExistence(timeout: 30), "the frame is there from the first frame")
        XCTAssertTrue(element(app, "now-drive-just-started").exists, "the short state")
        XCTAssertTrue(element(app, "now-drive-since").exists, "since alone in the header")
        XCTAssertFalse(element(app, "now-drive-top").exists, "no line yet")
    }

    func testNoTripNoDestinationLine() {
        let app = launch(dest: false)
        XCTAssertTrue(element(app, "now-drive-graph").waitForExistence(timeout: 30))
        XCTAssertFalse(element(app, "now-drive-dest").exists)
    }

    /// The parity shots: the same states as the web's e2e matrix (web/e2e/now-drive-graph.pw.ts), filed by hand under _shared/drive-graph/ios.
    func testParityShots() throws {
        guard ProcessInfo.processInfo.environment["RIVOLT_PARITY_SHOTS"] != nil || FileManager.default.fileExists(atPath: "/tmp/rivolt-ui-shots/.parity") else { throw XCTSkip("shots on request") }
        let combos: [(String, Bool, Bool, Bool)] = [("middle", false, false, false), ("middle", true, false, true), ("stop", false, false, false), ("middle", false, true, false), ("fresh", false, false, false)]
        for (name, dark, dest, metric) in combos {
            let app = launch(name, dest: !dest, dark: dark, metric: metric)
            if name != "fresh" { XCTAssertTrue(element(app, "now-drive-graph").waitForExistence(timeout: 30), "\(name) graph") } else { Thread.sleep(forTimeInterval: 3) }
            let tag = "\(name)-\(dark ? "dark" : "light")-\(dest ? "notrip" : "trip")-\(metric ? "km" : "mi")"
            shot(app, "parity-\(tag)-top")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8)).press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35)))
            shot(app, "parity-\(tag)-graph")
            app.terminate()
        }
    }
}
