import XCTest
@testable import Rivolt

/// Figures round as the web's `toFixed` does: a half away from zero, and never grouped.
final class NumberTextTests: XCTestCase {
    func testAHalfRoundsAwayFromZeroNotToEven() {
        XCTAssertEqual(NumberText.fixed(0.125, 2), "0.13")
        XCTAssertEqual(NumberText.fixed(2.25, 1), "2.3")
        XCTAssertEqual(NumberText.fixed(2.5, 0), "3")
        XCTAssertEqual(NumberText.fixed(3.5, 0), "4")
        XCTAssertEqual(NumberText.fixed(-2.25, 1), "-2.3")
        XCTAssertEqual(NumberText.fixed(0.375, 2), "0.38")
    }

    func testDigitsAreNeverGrouped() {
        XCTAssertEqual(NumberText.fixed(1120.24, 1), "1120.2")
        XCTAssertEqual(NumberText.fixed(1234567.891, 1), "1234567.9")
    }

    func testMoneyRoundsTheSameWay() {
        XCTAssertEqual(NumberText.money(0.125, "USD"), "$0.13")
        XCTAssertEqual(NumberText.money(35.425, "USD"), "$35.43")
        XCTAssertEqual(NumberText.money(2.5, ""), "2.50", "no usable currency code: plain digits")
    }

    func testTheFormattersEveryScreenUsesGoThroughIt() {
        XCTAssertEqual(NowFormat.oneDecimal(2.25), "2.3")
        XCTAssertEqual(NowFormat.twoDecimals(0.125), "0.13")
        XCTAssertEqual(NowFormat.money(0.125, currency: "USD"), "$0.13")
    }
}
