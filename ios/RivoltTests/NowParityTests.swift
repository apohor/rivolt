import XCTest
@testable import Rivolt

/// The Now hero's wording and states, the same rules as the web's `nowState.ts`.
final class NowParityTests: XCTestCase {
    private let at = Date(timeIntervalSince1970: 1_800_000_000)

    private func summary(soc: Double = 60, gear: String = "P", power: String? = nil, kw: Double? = nil, sleep: Bool = false,
                         target: Double? = 80, age: TimeInterval = 20) -> NowSummary {
        let input = NowSummary.Input(socPct: soc, rangeKm: 200, chargeTargetPct: target, gear: gear, powerState: sleep ? "sleep" : "ready",
                                     chargerState: power, chargerPowerKw: kw, at: at.addingTimeInterval(-age), packKwh: 131)
        return NowSummary(input: input, now: nil, clock: at)
    }

    func testPresenceFollowsTheWebsLimitsPerState() {
        XCTAssertEqual(summary(age: 100).presence, .live)
        XCTAssertEqual(summary(age: 300).presence, .stale)
        XCTAssertEqual(summary(age: 1000).presence, .offline)
        XCTAssertEqual(summary(gear: "D", age: 40).presence, .stale)
        XCTAssertEqual(summary(gear: "D", age: 200).presence, .offline)
        XCTAssertEqual(summary(sleep: true, age: 1000).presence, .live, "a sleeping truck's frames are minutes apart")
        XCTAssertEqual(summary(sleep: true, age: 3000).presence, .offline)
        XCTAssertEqual(summary(power: "charger_connected", sleep: true, age: 1000).presence, .live, "a plugged-in truck that sleeps keeps the asleep cadence")
    }

    func testTheHeadlinesAreTheWebs() {
        XCTAssertEqual(NowCard.headline(for: summary(gear: "D")), "Driving")
        XCTAssertEqual(NowCard.headline(for: summary(power: "charging_active", kw: 7.2)), "Charging")
        XCTAssertEqual(NowCard.headline(for: summary(power: "charging_active", kw: 142)), "Fast charging")
        XCTAssertEqual(NowCard.headline(for: summary(power: "charging_active", kw: 0.1)), "Starting to charge…")
        XCTAssertEqual(NowCard.headline(for: summary(soc: 80, power: "charger_connected", kw: 0)), "Plugged in, holding 80%")
        XCTAssertEqual(NowCard.headline(for: summary(soc: 55, power: "charger_connected", kw: 0)), "Plugged in")
        XCTAssertEqual(NowCard.headline(for: summary(sleep: true)), "Asleep")
        XCTAssertEqual(NowCard.headline(for: summary()), "Parked")
        XCTAssertEqual(NowCard.headline(for: summary(age: 5000)), "Not heard from recently")
    }

    func testThePillSaysLiveAsleepLastSeenOrOffline() {
        XCTAssertEqual(NowCard.pill(for: summary(), clock: at).text, "Live")
        XCTAssertEqual(NowCard.pill(for: summary(sleep: true, age: 600), clock: at).text, "Asleep")
        let stale = NowCard.pill(for: summary(age: 300), clock: at)
        XCTAssertEqual(stale.text, "Last seen 5 min ago")
        XCTAssertEqual(stale.tone, .stale)
        XCTAssertEqual(NowCard.pill(for: summary(age: 5000), clock: at).text, "Offline")
    }

    func testTheCriticalWarningNeedsUnderTenPercentAndAnOnlineTruck() {
        XCTAssertTrue(NowCard.isCritical(summary(soc: 9)))
        XCTAssertFalse(NowCard.isCritical(summary(soc: 10)))
        XCTAssertFalse(NowCard.isCritical(summary(soc: 5, age: 5000)), "an offline truck's last level isn't a warning")
        let alerts = NowCard.alerts(for: summary(soc: 7), vehicleName: "R1S")
        XCTAssertEqual(alerts.map(\.text), ["Battery at 7%. Plan a charge soon."])
        XCTAssertEqual(alerts.first?.tone, .rose)
    }

    func testTheFooterSaysHowFreshAndSinceWhen() {
        XCTAssertEqual(NowCard.footer(for: summary(age: 20), clock: at), "Updated just now")
        let offline = NowCard.footer(for: summary(age: 47 * 3600), clock: at)
        XCTAssertTrue(offline.hasPrefix("Last contact "), offline)
        XCTAssertTrue(offline.hasSuffix("(47 h ago)"), offline)
        XCTAssertEqual(NowCard.eta(minutes: 42), "~42 min")
        XCTAssertEqual(NowCard.eta(minutes: 306), "~5 h 6 m")
        XCTAssertEqual(NowFormat.ago(90), "1 min ago")
        XCTAssertEqual(NowFormat.ago(3 * 3600), "3 h ago")
        XCTAssertEqual(NowFormat.ago(50 * 3600), "2 d ago")
    }
}
