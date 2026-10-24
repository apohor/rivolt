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

    func testTheListsAreReadAgainAfterADriveOrChargeEnds() {
        XCTAssertEqual(NowRefresh.afterSessionEndsDelays, [30, 90, 180, 360], "the server saves the session a little after the truck stops")
        XCTAssertEqual(NowRefresh.afterSessionEndsDelays, NowRefresh.afterSessionEndsDelays.sorted())
        XCTAssertTrue(NowRefresh.inFlight(.charging))
        XCTAssertTrue(NowRefresh.inFlight(.driving))
        for kind in [NowSnapshot.State.plugged, .parked, .asleep, .unknown] { XCTAssertFalse(NowRefresh.inFlight(kind), "\(kind)") }
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
        // The re-read: the server has a frame from two and a half minutes ago.
        freshness.fetched(at: late)
        let reread = parked(at: late.addingTimeInterval(-150), clock: late)
        XCTAssertFalse(freshness.holdsOffline(reread, clock: late))
        XCTAssertEqual(freshness.shown(reread, clock: late).presence, .stale, "past the web's two-minute limit: stale, not offline")
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

    // MARK: The server's real shapes

    /// `/api/vehicles/{id}/now` and `/api/state/{id}` as the server sends them (the public demo's answers, with the
    /// coordinates and the vehicle id taken out): timestamps with six and with nine fractional digits.
    func testTheServersRealNowAndStateShapesDecodeAndReadLiveWithAFreshClock() throws {
        let st = try Fixture.decode(VehicleState.self, "state_prod_shape")
        let nw = try Fixture.decode(NowSnapshot.self, "now_prod_shape")
        let at = try XCTUnwrap(nw.at)
        XCTAssertEqual(at.timeIntervalSince1970, try XCTUnwrap(JSON.parseDate("2026-10-23T14:07:45.589Z")).timeIntervalSince1970, accuracy: 0.01, "six fractional digits")
        XCTAssertEqual(try XCTUnwrap(st.at).timeIntervalSince1970, at.timeIntervalSince1970, accuracy: 0.01, "nine fractional digits")
        XCTAssertNotNil(nw.vehicleStateFrameAt, "nine fractional digits too")
        let clock = at.addingTimeInterval(30)
        XCTAssertEqual(NowSummary(state: st, now: nw, clock: clock).presence, .live)
        XCTAssertEqual(NowSummary(state: st, now: nil, clock: clock).presence, .live, "if /now were unreadable the state alone says live")
        XCTAssertEqual(NowSummary(state: nil, now: nw, clock: clock).presence, .live, "and /now alone")
    }

    func testAParkedTrucksRealShapeStaysLiveForTwoMinutesAndGoesStaleThenOffline() throws {
        // The same answers for a parked, ready truck (not asleep), as the owner's R1S reports.
        func text(_ name: String) throws -> String { String(decoding: try Fixture.data(name), as: UTF8.self) }
        let nowJSON = try text("now_prod_shape").replacingOccurrences(of: "\"state\": \"asleep\"", with: "\"state\": \"parked\"")
            .replacingOccurrences(of: "\"asleep\": true", with: "\"asleep\": false").replacingOccurrences(of: "\"connection\": \"replay\"", with: "\"connection\": \"live\"")
        let stateJSON = try text("state_prod_shape").replacingOccurrences(of: "\"power_state\": \"sleep\"", with: "\"power_state\": \"ready\"")
        let nw = try JSON.decoder().decode(NowSnapshot.self, from: Data(nowJSON.utf8))
        let st = try JSON.decoder().decode(VehicleState.self, from: Data(stateJSON.utf8))
        XCTAssertEqual(nw.state, .parked)
        XCTAssertEqual(nw.connection, .live)
        let at = try XCTUnwrap(nw.at)
        XCTAssertEqual(NowSummary(state: st, now: nw, clock: at.addingTimeInterval(60)).presence, .live)
        XCTAssertEqual(NowSummary(state: st, now: nw, clock: at.addingTimeInterval(300)).presence, .stale)
        XCTAssertEqual(NowSummary(state: st, now: nw, clock: at.addingTimeInterval(901)).presence, .offline, "the web's limit for a parked truck")
    }

    // MARK: What is logged

    func testALoggedErrorIsAFixedPhraseNeverTheErrorsOwnText() {
        let url = URL(string: "https://rivolt.example.org/api/state/veh-123456")!
        let urlError = URLError(.timedOut, userInfo: [NSURLErrorFailingURLErrorKey: url, NSLocalizedDescriptionKey: "The request to \(url) timed out."])
        XCTAssertTrue(urlError.localizedDescription.contains("veh-123456"), "an error's own text carries the URL: that is why it is not logged")
        let line = NowReads.describe(urlError)
        XCTAssertEqual(line, "timed out")
        XCTAssertFalse(line.contains("veh-123456") || line.contains("rivolt.example.org") || line.contains("http"))
        XCTAssertEqual(NowReads.describe(URLError(.notConnectedToInternet)), "no network")
        XCTAssertEqual(NowReads.describe(URLError(.cannotFindHost)), "can't reach the server")
        XCTAssertEqual(NowReads.describe(URLError(.badServerResponse)), "network error")
        XCTAssertEqual(NowReads.describe(APIError.server(status: 503, message: "boom for veh-123456")), "HTTP 503", "a server's message body is not logged")
        XCTAssertEqual(NowReads.describe(APIError.transport("https://rivolt.example.org/api/state/veh-123456 failed")), "no answer")
        XCTAssertEqual(NowReads.describe(APIError.badURL), "bad URL")
        XCTAssertEqual(NowReads.describe(NSError(domain: "x", code: 1, userInfo: [NSLocalizedDescriptionKey: "secret"])), "failed")
    }

    func testADecodingErrorIsLoggedAsItsKindAndFieldPathOnly() throws {
        let json = #"{"at":"2026-10-23T14:07:45Z","connection":"live","state":"parked","failed_sleeps_6h":0}"#   // no "version"
        do {
            _ = try JSON.decoder().decode(NowSnapshot.self, from: Data(json.utf8))
            XCTFail("expected a decoding error")
        } catch {
            let line = NowReads.describe(APIError.decoding(error))
            XCTAssertEqual(line, "unreadable: missing version")
            XCTAssertFalse(line.contains("2026-10-23"), "no values from the payload")
        }
        let bad = #"{"version":1,"at":"not a date","connection":"live","state":"parked","failed_sleeps_6h":0}"#
        do {
            _ = try JSON.decoder().decode(NowSnapshot.self, from: Data(bad.utf8))
            XCTFail("expected a decoding error")
        } catch {
            let line = NowReads.describe(error)
            XCTAssertEqual(line, "unreadable: corrupt at")
            XCTAssertFalse(line.contains("not a date"), "the offending value is not logged")
        }
    }
}
