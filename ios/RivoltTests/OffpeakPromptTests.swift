import XCTest
@testable import Rivolt

final class OffpeakPromptTests: XCTestCase {
    private func schedule(rate: Double? = 0.15, configured: Bool? = false, flat: Bool? = false) -> OffpeakSchedule {
        OffpeakSchedule(tz: "UTC", windows: [], configured: configured, baseRate: rate, currency: "USD", flatRate: flat)
    }

    func testItAppliesOnlyWithAHomeRateNoHoursAndNoFlatRateChoice() {
        XCTAssertTrue(OffpeakPromptLogic.applies(schedule()))
        XCTAssertFalse(OffpeakPromptLogic.applies(schedule(rate: 0)), "no home rate")
        XCTAssertFalse(OffpeakPromptLogic.applies(schedule(rate: nil)))
        XCTAssertFalse(OffpeakPromptLogic.applies(schedule(configured: true)), "hours are saved")
        XCTAssertFalse(OffpeakPromptLogic.applies(schedule(configured: nil)), "an answer that doesn't say")
        XCTAssertFalse(OffpeakPromptLogic.applies(schedule(flat: true)), "one flat rate")
        XCTAssertFalse(OffpeakPromptLogic.applies(nil))
    }

    func testItIsOnceNotInTheDemoAndGoneAfterNotNow() {
        XCTAssertTrue(OffpeakPromptLogic.shows(schedule(), dismissed: false, demo: false))
        XCTAssertFalse(OffpeakPromptLogic.shows(schedule(), dismissed: true, demo: false), "Not now is remembered")
        XCTAssertFalse(OffpeakPromptLogic.shows(schedule(), dismissed: false, demo: true))
    }

    func testTheWordsAreGrowthsAndSayEstimate() {
        XCTAssertEqual(OffpeakPromptLogic.title, "Do you have cheaper hours?")
        XCTAssertTrue(OffpeakPromptLogic.body.hasSuffix("as an estimate from your own charges."))
        XCTAssertEqual(OffpeakPromptLogic.setButton, "Set off-peak hours")
        XCTAssertEqual(OffpeakPromptLogic.laterButton, "Not now")
        XCTAssertEqual(OffpeakPromptLogic.dismissedKey, "rivolt.offpeakPrompt.dismissed")
    }
}
