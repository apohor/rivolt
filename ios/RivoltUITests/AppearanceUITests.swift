import XCTest

/// Settings' first row: Auto / Light / Dark as one segmented control (a menu where the text is too large for three segments),
/// driven like a person would with every call answered from fixtures (Rivolt/App/UITestFixtures.swift).
final class AppearanceUITests: XCTestCase {
    private func launch(_ arguments: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        let ios = here.deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path, ios.appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += arguments
        app.launch()
        return app
    }

    private func openSettings(_ app: XCUIApplication) -> XCUIElement {
        let button = app.buttons["settings-button"].firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 20), "the person icon is there")
        button.tap()
        let picker = app.descendants(matching: .any)["appearance-picker"].firstMatch
        XCTAssertTrue(picker.waitForExistence(timeout: 10), "Settings opens on the appearance control")
        return picker
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        try? FileManager.default.createDirectory(atPath: "/tmp/claude-501/shots", withIntermediateDirectories: true)
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/claude-501/shots/\(name).png"))
    }

    func testOneTapFromSettingsSwitchesAutoLightDark() {
        let app = launch()
        let picker = openSettings(app)
        XCTAssertTrue(picker.buttons["Auto"].exists && picker.buttons["Light"].exists && picker.buttons["Dark"].exists, "three segments")
        picker.buttons["Auto"].tap()  // the simulator keeps the last run's choice
        XCTAssertTrue(picker.buttons["Auto"].isSelected)
        shot(app, "appearance-auto")
        picker.buttons["Light"].tap()
        XCTAssertTrue(picker.buttons["Light"].isSelected)
        shot(app, "appearance-light")
        picker.buttons["Dark"].tap()
        XCTAssertTrue(picker.buttons["Dark"].isSelected)
        shot(app, "appearance-dark")
    }

    func testAtLargestTextTheChoiceIsStillThereAndFits() {
        let app = launch(["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])
        let picker = openSettings(app)
        XCTAssertTrue(picker.isHittable, "the control can be tapped at the largest text size")
        XCTAssertLessThanOrEqual(picker.frame.maxX, app.windows.firstMatch.frame.width, "inside the screen")
        shot(app, "appearance-ax3")
    }
}
