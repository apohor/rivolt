import XCTest

/// The App Review screenshot for the Rivolt Pro subscriptions (App Store Connect asks for one of the screen that sells them): the Pro sheet
/// with the monthly and yearly choice, the renewal terms and Terms and Privacy, in light. It is the account with no vehicle, billing on, free tier,
/// answered from fixtures like MyVehiclesUITests. ios/tools/review_shot.sh runs it on the 6.9 inch simulator and keeps the PNG in
/// ios/store/review-subscription.png (RIVOLT_REVIEW_SHOT_OUT names where to write it); without that variable the test only attaches the picture.
final class ReviewShotUITests: XCTestCase {
    func testTheProSheetWithBothChoicesIsShot() throws {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path,
                                                          here.deletingLastPathComponent().appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchEnvironment["RIVOLT_UITEST_NO_TRUCK"] = "none"
        app.launchEnvironment["RIVOLT_UITEST_BILLING"] = "1"
        app.launchArguments += ["-rivolt.appearance", "light"]
        app.launch()
        let pro = app.buttons["Rivolt Pro"].firstMatch
        XCTAssertTrue(pro.waitForExistence(timeout: 30), "the Pro entry is on the no-vehicle screen")
        pro.tap()
        let yearly = app.buttons["Yearly, $34.99 a year"].firstMatch
        let monthly = app.buttons["Monthly, $3.99 a month"].firstMatch
        XCTAssertTrue(yearly.waitForExistence(timeout: 20), "the yearly choice with its price")
        XCTAssertTrue(monthly.exists, "the monthly choice with its price")
        // The sheet opens at the top, above the choices: scroll until both are on screen, so the picture shows what is sold, at what price.
        var tries = 0
        while !(yearly.isHittable && monthly.isHittable) && tries < 8 { app.swipeUp(velocity: .slow); tries += 1 }
        XCTAssertTrue(yearly.isHittable && monthly.isHittable, "both choices are on screen")
        assertRendered(app, dark: false)
        let screenshot = app.screenshot()
        let a = XCTAttachment(screenshot: screenshot)
        a.name = "review-subscription"
        a.lifetime = .keepAlways
        add(a)
        if let out = ProcessInfo.processInfo.environment["RIVOLT_REVIEW_SHOT_OUT"] {
            try screenshot.pngRepresentation.write(to: URL(fileURLWithPath: out))
        }
    }
}
