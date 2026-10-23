import XCTest
@testable import Rivolt

/// "Offline" is the truck's word: a parked truck whose server frame is two minutes old must never read Offline, and a
/// truck that really has gone quiet must, with the clock injected so no test waits.
final class NowRefreshTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    /// A parked truck (ready, in Park, unplugged) whose newest frame is at `at`.
    private func parked(at: Date, clock: Date) -> NowSummary {
        let input = NowSummary.Input(socPct: 72, rangeKm: 280, chargeTargetPct: 80, gear: "P", powerState: "ready",
                                     chargerState: nil, chargerPowerKw: nil, at: at, packKwh: 131)
        return NowSummary(input: input, now: nil, clock: clock)
    }

    func testTheScreenReadsQuicklyWhileItMovesAndEveryMinuteOtherwise() {
        XCTAssertEqual(NowRefresh.pollInterval(.charging), 10)
        XCTAssertEqual(NowRefresh.pollInterval(.driving), 10)
        for kind in [NowSnapshot.State.parked, .plugged, .asleep, .unknown] {
            XCTAssertEqual(NowRefresh.pollInterval(kind), 60, "\(kind)")
        }
    }

    func testAParkedTruckWithAFreshServerFrameIsNeverOfflineOverSixteenMinutes() {
        var freshness = NowFreshness()
        var clock = t0
        for minute in 0...16 {
            clock = t0.addingTimeInterval(Double(minute) * 60)
            // The poll: the server's newest frame is a few seconds old.
            freshness.fetched(at: clock)
            let shown = freshness.shown(parked(at: clock.addingTimeInterval(-5), clock: clock), clock: clock)
            XCTAssertNotEqual(shown.presence, .offline, "minute \(minute)")
            XCTAssertEqual(shown.presence, .live, "minute \(minute)")
        }
    }

    func testWithoutAReadTheCardWouldHaveWalkedToOfflineSoItIsHeldAndTheScreenReadsAgain() {
        // The bug: one read at the start, nothing after (the poll only ran while charging or driving).
        var freshness = NowFreshness()
        freshness.fetched(at: t0)
        let late = t0.addingTimeInterval(16 * 60)
        let raw = parked(at: t0.addingTimeInterval(-5), clock: late)
        XCTAssertEqual(raw.presence, .offline, "what the old card said, with a frame the server had refreshed long ago")
        XCTAssertTrue(freshness.holdsOffline(raw, clock: late), "the last read is 16 minutes old: ask again before saying Offline")
        XCTAssertEqual(freshness.shown(raw, clock: late).presence, .stale)
        // The re-read: the server has a frame from two minutes ago.
        freshness.fetched(at: late)
        let reread = parked(at: late.addingTimeInterval(-120), clock: late)
        XCTAssertFalse(freshness.holdsOffline(reread, clock: late))
        XCTAssertEqual(freshness.shown(reread, clock: late).presence, .stale, "two minutes old: stale by the web's limit, not offline")
        let fresher = parked(at: late.addingTimeInterval(-3), clock: late)
        XCTAssertEqual(freshness.shown(fresher, clock: late).presence, .live)
    }

    func testATruckThatReallyWentQuietIsOfflineOnceTheServerHasBeenAskedAgain() {
        var freshness = NowFreshness()
        let seen = t0   // the server's newest frame never changes
        for minute in 0...20 {
            let clock = t0.addingTimeInterval(Double(minute) * 60)
            freshness.fetched(at: clock)   // polled every minute, same answer
            let shown = freshness.shown(parked(at: seen, clock: clock), clock: clock)
            if minute * 60 > 900 {
                XCTAssertEqual(shown.presence, .offline, "minute \(minute): asked a moment ago, still nothing newer")
            } else {
                XCTAssertNotEqual(shown.presence, .offline, "minute \(minute)")
            }
        }
    }

    func testAnOldReadHoldsAnOfflineBackAndARecentOneLetsItThrough() {
        var freshness = NowFreshness()
        let clock = t0.addingTimeInterval(2_000)
        let silent = parked(at: t0, clock: clock)
        XCTAssertEqual(silent.presence, .offline)
        XCTAssertTrue(freshness.holdsOffline(silent, clock: clock), "no read yet")
        freshness.fetched(at: clock.addingTimeInterval(-NowFreshness.confirmWindow - 1))
        XCTAssertTrue(freshness.holdsOffline(silent, clock: clock), "the last read is older than the window")
        freshness.fetched(at: clock.addingTimeInterval(-30))
        XCTAssertFalse(freshness.holdsOffline(silent, clock: clock), "asked 30 seconds ago and still silent: Offline is true")
        // A truck that is not offline is never held.
        XCTAssertFalse(NowFreshness().holdsOffline(parked(at: clock.addingTimeInterval(-10), clock: clock), clock: clock))
    }

    func testNoFrameAtAllIsHeldUntilTheServerHasBeenAsked() {
        let none = NowSummary(input: nil, now: nil, clock: t0)
        XCTAssertEqual(none.presence, .offline)
        XCTAssertEqual(NowFreshness().shown(none, clock: t0).presence, .stale, "not read yet: not Offline")
        var read = NowFreshness()
        read.fetched(at: t0)
        XCTAssertEqual(read.shown(none, clock: t0).presence, .offline, "read just now, and there is nothing")
    }
}
