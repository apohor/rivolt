import XCTest
@testable import Rivolt

/// While driving, /state is read every 2 s and /now every 10 s, so /now can say "parked" for a few seconds after the truck is in gear. The card must not
/// read "Parked" with "Last seen just now" then (QA, iPhone SE against preview), and a failed /now must not drop what is known.
@MainActor
final class NowDrivingRaceTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func now(_ state: String, at: Date) throws -> NowSnapshot {
        let iso = ISO8601DateFormatter().string(from: at)
        return try JSON.decoder().decode(NowSnapshot.self, from: Data(#"{"version":1,"at":"\#(iso)","connection":"live","state":"\#(state)","failed_sleeps_6h":0}"#.utf8))
    }

    private func frame(gear: String, at: Date, kph: Double? = nil) -> NowSummary.Input {
        .init(socPct: 60, rangeKm: 250, chargeTargetPct: 80, gear: gear, powerState: "ready", chargerState: "charger_disconnected", chargerPowerKw: 0, at: at, packKwh: 100, speedKph: kph)
    }

    /// The response sequence: /now answers parked at t0, then /state frames in gear arrive at t0+2 and t0+4 before the next /now at t0+10.
    func testAFrameInGearNewerThanNowBeatsAParkedNow() throws {
        let parked = try now("parked", at: t0)
        let atRest = NowSummary(input: frame(gear: "P", at: t0), now: parked, clock: t0)
        XCTAssertEqual(atRest.kind, .parked)

        for (secs, kph) in [(2.0, 8.0), (4.0, 31.0)] {
            let at = t0.addingTimeInterval(secs)
            let s = NowSummary(input: frame(gear: "D", at: at, kph: kph), now: parked, clock: at)
            XCTAssertEqual(s.kind, .driving, "t0+\(secs): in gear on a newer frame")
            XCTAssertEqual(s.stateLabel, "Driving")
            XCTAssertEqual(s.presence, .live)
            XCTAssertNil(s.since, "the parked since is not when the drive began")
            XCTAssertEqual(s.speedKph, kph)
        }
        let caught = try now("driving", at: t0.addingTimeInterval(10))
        XCTAssertEqual(NowSummary(input: frame(gear: "D", at: t0.addingTimeInterval(10)), now: caught, clock: t0.addingTimeInterval(10)).kind, .driving)
    }

    func testAnOlderFrameInGearDoesNotOverrideANewerNow() throws {
        let parked = try now("parked", at: t0.addingTimeInterval(5))
        XCTAssertEqual(NowSummary(input: frame(gear: "D", at: t0), now: parked, clock: t0.addingTimeInterval(5)).kind, .parked)
    }

    func testOnlyGearMovesItAndOnlyTowardDriving() throws {
        let later = t0.addingTimeInterval(3)
        XCTAssertEqual(NowSummary(input: frame(gear: "P", at: later), now: try now("driving", at: t0), clock: later).kind, .driving, "a late Driving after P is harmless")
        XCTAssertEqual(NowSummary(input: frame(gear: "P", at: later), now: try now("charging", at: t0), clock: later).kind, .charging)
        XCTAssertEqual(NowSummary(input: frame(gear: "R", at: later), now: try now("plugged", at: t0), clock: later).kind, .driving)
    }

    func testAFailedNowKeepsTheLastOneForTheSameTruckOnly() throws {
        let driving = try now("driving", at: t0)
        XCTAssertEqual(NowReads.nowToShow(fresh: nil, last: driving, lastID: "a", id: "a"), driving)
        XCTAssertNil(NowReads.nowToShow(fresh: nil, last: driving, lastID: "a", id: "b"), "another truck never inherits it")
        let fresh = try now("parked", at: t0)
        XCTAssertEqual(NowReads.nowToShow(fresh: fresh, last: driving, lastID: "a", id: "a"), fresh)
    }
}
