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

    /// The drives read count once it has stopped moving for `quiet` seconds (20 s at most). A read is async, so a fixed sleep either cuts one off on
    /// a slow runner or waits for nothing on a fast one; this ends when the trace is quiet, however long that takes.
    private func settledReads(_ trace: URL, quiet: TimeInterval = 1.5) -> Int {
        let end = Date().addingTimeInterval(20)
        var last = reads(trace, "/api/drives"), since = Date()
        while Date() < end {
            RunLoop.current.run(until: Date().addingTimeInterval(0.25))
            let now = reads(trace, "/api/drives")
            if now != last { last = now; since = Date() } else if Date().timeIntervalSince(since) >= quiet { break }
        }
        return last
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
        let opened = settledReads(trace)
        XCTAssertGreaterThanOrEqual(opened, 1, "opening the map reads its drives")
        pick("List", in: app)
        pick("Map", in: app)
        XCTAssertEqual(settledReads(trace), opened, "back on the map with no pull in between, it keeps what it has")
        pick("List", in: app)
        let window = app.windows.firstMatch
        window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.35))
            .press(forDuration: 0.1, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8)), withVelocity: .slow, thenHoldForDuration: 0.3)
        _ = settledReads(trace)
        pick("Map", in: app)
        XCTAssertEqual(waitForReads(trace, opened + 1), opened + 1, "after a pull in the list the map reads its drives again")
    }
}
