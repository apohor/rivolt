import XCTest

/// Activity's map has no pull of its own (a map pans): a pull in the list marks it, and it reads again when opened. Counted from the fixture trace
/// (RIVOLT_UITEST_TRACE: every request the app made).
final class ActivityRefreshUITests: XCTestCase {
    private func reads(_ trace: URL, _ path: String) -> Int {
        let text = (try? String(contentsOf: trace, encoding: .utf8)) ?? ""
        return text.split(separator: "\n").filter { $0.hasSuffix(" GET \(path)") }.count
    }

    /// Polls the trace until the drives list has been read `count` times (or 20 s pass); returns how many reads there are.
    private func waitForReads(_ trace: URL, _ count: Int) -> Int {
        let end = Date().addingTimeInterval(20)
        while reads(trace, "/api/drives") < count && Date() < end { Thread.sleep(forTimeInterval: 0.25) }
        return reads(trace, "/api/drives")
    }

    private func pick(_ name: String, in app: XCUIApplication) {
        let button = app.buttons["View, \(name)"].firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 20), "\(name) is in the view switch")
        button.tap()
    }

    func testAPullInTheListMakesTheMapReadAgainWhenOpened() throws {
        let trace = FileManager.default.temporaryDirectory.appendingPathComponent("activity-trace-\(UUID().uuidString).txt")
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path,
                                                          here.deletingLastPathComponent().appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchEnvironment["RIVOLT_UITEST_TRACE"] = trace.path
        app.launch()
        let tab = app.buttons["Activity"].firstMatch
        XCTAssertTrue(tab.waitForExistence(timeout: 20), "the Activity tab is there")
        tab.tap()
        pick("Map", in: app)
        _ = waitForReads(trace, 1)
        Thread.sleep(forTimeInterval: 3)
        let opened = reads(trace, "/api/drives")
        XCTAssertGreaterThanOrEqual(opened, 1, "opening the map reads its drives")
        pick("List", in: app)
        pick("Map", in: app)
        Thread.sleep(forTimeInterval: 2)
        XCTAssertEqual(reads(trace, "/api/drives"), opened, "back on the map with no pull in between, it keeps what it has")
        pick("List", in: app)
        let window = app.windows.firstMatch
        window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35))
            .press(forDuration: 0.1, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8)), withVelocity: .slow, thenHoldForDuration: 0.3)
        Thread.sleep(forTimeInterval: 2)
        pick("Map", in: app)
        XCTAssertEqual(waitForReads(trace, opened + 1), opened + 1, "after a pull in the list the map reads its drives again")
    }
}
