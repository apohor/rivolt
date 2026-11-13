import XCTest

/// With design_preview ios_now_v2: the Now screen's new look (the temperatures line, the 2x2 month, no "Live" in the status chip),
/// driven with every call answered from fixtures (Rivolt/App/UITestFixtures.swift). The truck picture is not fetched here (no network): the hero shows no picture.
final class NowV2UITests: XCTestCase {
    private func launch(_ arguments: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += arguments
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

    func testNowV2ShowsTheNewLayout() {
        let app = launch(["-uitest-now-v2"])
        XCTAssertTrue(app.descendants(matching: .any)["now-month"].firstMatch.waitForExistence(timeout: 20), "the 2x2 month strip")
        XCTAssertFalse(app.buttons["now-live-button"].exists, "the iPhone has no Live button until it has a native Live screen")
        XCTAssertFalse(app.staticTexts["Live"].exists && app.staticTexts.matching(NSPredicate(format: "label == 'Live'")).count > 1, "the status chip never says Live")
        shot(app, "now-v2-top")
        app.swipeUp()
        shot(app, "now-v2-scrolled")
        app.swipeUp()
        shot(app, "now-v2-bottom")
    }

    func testASignedInRelaunchStillShowsTheNewLayout() {
        let first = launch(["-uitest-now-v2"])
        XCTAssertTrue(first.descendants(matching: .any)["now-month"].firstMatch.waitForExistence(timeout: 20))
        first.terminate()
        let again = launch(["-uitest-now-v2"])
        XCTAssertTrue(again.descendants(matching: .any)["now-month"].firstMatch.waitForExistence(timeout: 20), "the keys are read again on a relaunch")
        XCUIDevice.shared.press(.home)
        again.activate()
        XCTAssertTrue(again.descendants(matching: .any)["now-month"].firstMatch.waitForExistence(timeout: 20), "and on coming back to the front")
    }

    func testWithoutTheFlagTheClassicCardStays() {
        let app = launch()
        XCTAssertTrue(app.buttons["settings-button"].firstMatch.waitForExistence(timeout: 20))
        XCTAssertFalse(app.buttons["now-live-button"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["now-month"].exists)
        shot(app, "now-classic")
    }
}
