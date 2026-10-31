import XCTest
@testable import Rivolt

final class AccountMenuTests: XCTestCase {
    func testTheSettingsButtonIsNamedAndBigEnough() {
        XCTAssertEqual(AccountMenuLogic.settingsLabel, "Settings")
        XCTAssertGreaterThanOrEqual(AccountMenuLogic.tapSize, 44)
    }
}
