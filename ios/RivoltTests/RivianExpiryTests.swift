import XCTest
@testable import Rivolt

final class RivianExpiryTests: XCTestCase {
    private let us = Locale(identifier: "en_US")
    private let utc = TimeZone(identifier: "UTC")!

    private func status(_ days: Int?, at: String? = "2026-11-03T18:00:00Z", _ edit: (inout RivianStatus) -> Void = { _ in }) -> RivianStatus {
        var s = RivianStatus(enabled: true, authenticated: true, mfaPending: false, needsReauth: false, paused: false, sessionExpiresAt: at, sessionDaysLeft: days)
        edit(&s)
        return s
    }

    func testTheNoticeIsN49sLineFromSevenDays() {
        XCTAssertEqual(RivianExpiryLogic.notice(status(7), locale: us, timeZone: utc), "Your Rivian connection expires Nov 3.")
        XCTAssertEqual(RivianExpiryLogic.notice(status(0), locale: us, timeZone: utc), "Your Rivian connection expires Nov 3.")
        XCTAssertNil(RivianExpiryLogic.notice(status(8), locale: us, timeZone: utc))
        XCTAssertNil(RivianExpiryLogic.notice(status(90), locale: us, timeZone: utc))
    }

    func testNothingShowsWhenTheServerSendsNoField() {
        XCTAssertNil(RivianExpiryLogic.notice(nil, locale: us, timeZone: utc))
        XCTAssertNil(RivianExpiryLogic.notice(status(nil, at: nil), locale: us, timeZone: utc), "an older server")
    }

    func testItYieldsToEveryWorseState() {
        XCTAssertNil(RivianExpiryLogic.notice(status(3) { $0.needsReauth = true }, locale: us, timeZone: utc))
        XCTAssertNil(RivianExpiryLogic.notice(status(3) { $0.mfaPending = true }, locale: us, timeZone: utc))
        XCTAssertNil(RivianExpiryLogic.notice(status(3) { $0.paused = true }, locale: us, timeZone: utc))
        XCTAssertNil(RivianExpiryLogic.notice(status(3) { $0.authenticated = false }, locale: us, timeZone: utc))
        XCTAssertNil(RivianExpiryLogic.notice(status(3) { $0.enabled = false }, locale: us, timeZone: utc))
    }

    func testWithoutAReadableDateItSaysDaysInTheSameStyle() {
        XCTAssertEqual(RivianExpiryLogic.notice(status(3, at: nil), locale: us, timeZone: utc), "Your Rivian connection expires in 3 days.")
        XCTAssertEqual(RivianExpiryLogic.notice(status(1, at: "garbage"), locale: us, timeZone: utc), "Your Rivian connection expires in 1 day.")
        XCTAssertEqual(RivianExpiryLogic.notice(status(0, at: ""), locale: us, timeZone: utc), "Your Rivian connection expires today.")
    }

    func testTheDateIsInTheReadersZone() {
        XCTAssertEqual(RivianExpiryLogic.shortDate("2026-11-03T02:00:00Z", locale: us, timeZone: TimeZone(identifier: "America/Los_Angeles")!), "Nov 2")
        XCTAssertEqual(RivianExpiryLogic.shortDate("2026-11-03T02:00:00.123Z", locale: us, timeZone: utc), "Nov 3")
        XCTAssertNil(RivianExpiryLogic.shortDate(nil))
    }

    func testTheStatusDecodesWithOrWithoutTheNewFields() throws {
        let old = try JSON.decoder().decode(RivianStatus.self, from: Data(#"{"enabled":true,"authenticated":true,"mfa_pending":false}"#.utf8))
        XCTAssertNil(old.sessionDaysLeft)
        let new = try JSON.decoder().decode(RivianStatus.self, from: Data(#"{"enabled":true,"authenticated":true,"mfa_pending":false,"email":"x@y.z","session_expires_at":"2026-11-03T18:00:00Z","session_days_left":5}"#.utf8))
        XCTAssertEqual(new.sessionDaysLeft, 5)
        XCTAssertEqual(new.sessionExpiresAt, "2026-11-03T18:00:00Z")
        XCTAssertEqual(RivianExpiryLogic.reconnectPath, "/settings?tab=account")
    }
}
