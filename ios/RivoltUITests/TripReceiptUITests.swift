import XCTest

/// The Saved tab's Share opens the trip receipt (design-preview key trip_receipt, answered from fixtures): the card is made from the server's
/// receipt, the sketch switch is off every time, and Share is offered once the card is painted.
final class TripReceiptUITests: XCTestCase {
    func testSavedTripSharesAReceipt() {
        let app = XCUIApplication()
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        app.launchEnvironment["RIVOLT_UITEST_FIXTURES"] = [here.appendingPathComponent("Fixtures").path,
                                                          here.deletingLastPathComponent().appendingPathComponent("RivoltTests/Fixtures").path].joined(separator: ":")
        app.launchArguments += ["-uitest-trip-receipt"]
        app.launch()
        let plan = app.buttons["Plan"].firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 20), "the Plan tab is there")
        plan.tap()
        let share = app.buttons["Share Seattle to Spokane"].firstMatch
        XCTAssertTrue(share.waitForExistence(timeout: 20), "the saved trip has a Share button")
        share.tap()

        let card = app.descendants(matching: .any)["trip-receipt-preview"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 20), "the receipt is on screen")
        let label = NSPredicate(format: "label CONTAINS 'Trinidad area'")
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: label, object: card)], timeout: 20), .completed,
                       "the card reads from the server's receipt: \(card.label)")
        let sketch = app.switches.matching(NSPredicate(format: "label BEGINSWITH %@", "Include route sketch")).firstMatch
        XCTAssertTrue(sketch.waitForExistence(timeout: 20))
        XCTAssertEqual(sketch.value as? String, "0", "the route sketch is off on the first open")
        XCTAssertTrue(app.buttons["Share"].firstMatch.waitForExistence(timeout: 20), "the card is painted, so it can be shared")
        sketch.tap()
        XCTAssertEqual(sketch.value as? String, "1")
        if let dir = ProcessInfo.processInfo.environment["RECEIPT_SHOTS"] {
            sleep(2)
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("ios-receipt-sheet-\(ProcessInfo.processInfo.environment["RECEIPT_MODE"] ?? "x").png"))
        }
        app.buttons["Cancel"].firstMatch.tap()
        // The sheet is gone before the Share button is tapped again (a tap during the dismissal is lost, and the old switch would be read).
        XCTAssertTrue(sketch.waitForNonExistence(timeout: 20), "the sheet is dismissed")
        XCTAssertTrue(share.waitForExistence(timeout: 10), "back on the Saved list")
        share.tap()
        XCTAssertTrue(sketch.waitForExistence(timeout: 20), "the route sketch switch is offered again on the second open")
        XCTAssertEqual(sketch.value as? String, "0", "off again on every open")
    }
}
