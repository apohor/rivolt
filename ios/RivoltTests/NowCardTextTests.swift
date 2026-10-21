import XCTest
@testable import Rivolt

/// The Now card's wording: the status in two lines (no orphaned time) and the
/// peak shown only when it matters.
final class NowCardTextTests: XCTestCase {
    private func charging() throws -> NowSummary {
        let st = try Fixture.decode(VehicleState.self, "state")
        let now = try Fixture.decode(NowSnapshot.self, "now_v1")
        return NowSummary(state: st, now: now, clock: try XCTUnwrap(now.at).addingTimeInterval(60))
    }

    func testTheStatusIsTwoLines() throws {
        let s = NowCard.status(for: try charging())
        XCTAssertEqual(s.headline, "Charging · 11.1 kW")
        let detail = try XCTUnwrap(s.detail)
        XCTAssertTrue(detail.hasPrefix("to 80%"), detail)
        XCTAssertTrue(detail.contains("since"), detail)
        XCTAssertFalse(s.headline.contains("since"))
    }

    func testAStatusWithNothingMoreHasNoSecondLine() throws {
        let s = NowSummary(state: nil, now: nil, clock: .now)
        let t = NowCard.status(for: s)
        XCTAssertNil(t.detail)
        XCTAssertEqual(t.headline, s.stateLabel)
    }

    func testThePeakShowsOnlyWhenClearlyAboveThePower() {
        XCTAssertEqual(NowCard.chargeLine(added: 21.9, peakKw: 11.2, powerKw: 11.1), "+22% this charge")
        XCTAssertEqual(NowCard.chargeLine(added: 40, peakKw: 150, powerKw: 90), "+40% this charge · peak 150.0 kW")
        XCTAssertEqual(NowCard.chargeLine(added: 10, peakKw: 7.2, powerKw: nil), "+10% this charge · peak 7.2 kW")
        XCTAssertEqual(NowCard.chargeLine(added: 10, peakKw: nil, powerKw: 7), "+10% this charge")
    }
}
