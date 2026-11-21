import XCTest

extension XCUIElement {
    /// A chip's selected state follows its tap by a render; a slow CI simulator showed it a beat late (PlanSheetUITests failed once with "the day
    /// chip took the tap" and passed on the re-run). 20 s like the other waits: a pass returns at once, a lost tap still fails.
    func becomesSelected(_ selected: Bool = true, timeout: TimeInterval = 20) -> Bool {
        let p = NSPredicate(format: "isSelected == %@", NSNumber(value: selected))
        return XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: p, object: self)], timeout: timeout) == .completed
    }

    /// A switch's value follows its tap by a render (the card repaints under it): ShareMapToggleUITests read "0" 0.7 s after a delivered tap
    /// (the recording shows the tap landing) and passed on the re-run. Same 20 s: a pass returns at once, a lost tap still fails.
    func becomesValue(_ value: String, timeout: TimeInterval = 20) -> Bool {
        let p = NSPredicate(format: "value == %@", value)
        return XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: p, object: self)], timeout: timeout) == .completed
    }
}

extension XCTestCase {
    /// The parity suites name their shots "-dark-" and "-light-" and launch with -rivolt.appearance. The app defaults to dark when the flag is
    /// missing, so for a while the "light" shots were dark and nothing failed. Read the page background (a point at the left edge, halfway
    /// down, where no card reaches) from a screenshot and fail when its brightness does not match the name. Polls: the first frames of a
    /// launch can still show the launch screen.
    func assertRendered(_ app: XCUIApplication, dark: Bool, timeout: TimeInterval = 15, file: StaticString = #filePath, line: UInt = #line) {
        var seen = -1.0
        let end = Date().addingTimeInterval(timeout)
        repeat {
            seen = app.screenshot().backgroundBrightness
            if dark ? seen < 0.35 : seen > 0.65 { return }
            Thread.sleep(forTimeInterval: 0.5)
        } while Date() < end
        XCTFail("launched \(dark ? "dark" : "light") but the page background reads brightness \(String(format: "%.2f", seen)) (0 black, 1 white)", file: file, line: line)
    }
}

private extension XCUIScreenshot {
    /// Brightness 0...1 of the pixel 1 % in from the left edge, halfway down; -1 when it cannot be read.
    var backgroundBrightness: Double {
        guard let cg = image.cgImage, let data = cg.dataProvider?.data, let bytes = CFDataGetBytePtr(data), cg.bitsPerPixel >= 24 else { return -1 }
        let bpp = cg.bitsPerPixel / 8
        let x = max(1, cg.width / 100), y = cg.height / 2
        let o = y * cg.bytesPerRow + x * bpp
        let r = Double(bytes[o]), g = Double(bytes[o + 1]), b = Double(bytes[o + 2])
        return (0.299 * r + 0.587 * g + 0.114 * b) / 255
    }
}
