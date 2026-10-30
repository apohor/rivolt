import XCTest
@testable import Rivolt

final class ShareNudgeTests: XCTestCase {
    func testItShowsOnceWithSomethingToShareOutsideTheDemo() {
        XCTAssertTrue(ShareNudgeLogic.shows(empty: false, canShare: true, demo: false, dismissed: false))
        XCTAssertFalse(ShareNudgeLogic.shows(empty: true, canShare: true, demo: false, dismissed: false), "an empty month has nothing to share")
        XCTAssertFalse(ShareNudgeLogic.shows(empty: false, canShare: false, demo: false, dismissed: false))
        XCTAssertFalse(ShareNudgeLogic.shows(empty: false, canShare: true, demo: true, dismissed: false))
        XCTAssertFalse(ShareNudgeLogic.shows(empty: false, canShare: true, demo: false, dismissed: true), "Not now, or the sheet opened")
    }

    func testTheWordsAreGrowthsWithNoFiguresAndNoComparing() {
        XCTAssertEqual(ShareNudgeLogic.title, "Share what charging cost this month")
        XCTAssertEqual(ShareNudgeLogic.body, "Your statement card shows the month's kWh and miles, with cost left out unless you add it. Nothing is posted; you pick where it goes.")
        let words = ShareNudgeLogic.title + ShareNudgeLogic.body
        XCTAssertNil(words.range(of: #"[\d$]|compare"#, options: [.regularExpression, .caseInsensitive]))
        XCTAssertEqual(ShareNudgeLogic.dismissedKey, "rivolt.shareNudge.dismissed")
    }
}
