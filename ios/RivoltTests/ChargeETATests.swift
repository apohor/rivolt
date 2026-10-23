import XCTest
@testable import Rivolt

/// The time left in a charge: the rule shared with the server and the web (charge_eta_vectors.json), and how Now says it.
final class ChargeETATests: XCTestCase {
    private struct Vectors: Decodable {
        struct Pick: Decodable {
            let name: String
            let truckMins: Double
            let validity: String
            let socPct: Double
            let limitPct: Double
            let oursMins: Double
            let wantMins: Double
            let wantSource: String
        }

        struct Own: Decodable {
            let name: String
            let socPct: Double
            let limitPct: Double
            let packKwh: Double
            let powerKw: Double
            let wantMins: Double
        }

        let pick: [Pick]
        let own: [Own]
    }

    func testSharedVectors() throws {
        let v = try Fixture.decode(Vectors.self, "charge_eta_vectors")
        XCTAssertGreaterThanOrEqual(v.pick.count, 15)
        XCTAssertGreaterThanOrEqual(v.own.count, 5)
        for c in v.pick {
            let r = ChargeETA.pick(truckMins: c.truckMins, validity: c.validity, socPct: c.socPct, limitPct: c.limitPct, oursMins: c.oursMins)
            XCTAssertEqual(r?.minutes ?? 0, c.wantMins, c.name)
            let source: String = r.map { $0.source == .truck ? "truck" : "estimate" } ?? ""
            XCTAssertEqual(source, c.wantSource, c.name)
        }
        for c in v.own {
            XCTAssertEqual(ChargeETA.ownMinutes(socPct: c.socPct, limitPct: c.limitPct, packKwh: c.packKwh, powerKw: c.powerKw), c.wantMins, c.name)
        }
    }

    private func summary(truck: Double?, validity: String?) -> NowSummary {
        let input = NowSummary.Input(socPct: 50, rangeKm: 200, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charging_active",
                                     chargerPowerKw: 7.2, at: Date(timeIntervalSince1970: 1_800_000_000), packKwh: 135,
                                     timeToEndMins: truck, timeToEndValidity: validity)
        return NowSummary(input: input, now: nil, clock: Date(timeIntervalSince1970: 1_800_000_000))
    }

    func testNowPrefersTheTrucksOwnFigureWhenItIsValid() {
        XCTAssertEqual(summary(truck: nil, validity: nil).chargeEta, ChargeETA.Result(minutes: 338, source: .estimate))
        XCTAssertEqual(summary(truck: 360, validity: "valid").chargeEta, ChargeETA.Result(minutes: 360, source: .truck))
        // Invalid, or implausibly short: ours, labelled as an estimate.
        XCTAssertEqual(summary(truck: 360, validity: "invalid").chargeEta, ChargeETA.Result(minutes: 338, source: .estimate))
        XCTAssertEqual(summary(truck: 20, validity: "valid").chargeEta, ChargeETA.Result(minutes: 338, source: .estimate))
    }

    func testTheReadoutSaysWhoseFigureItIs() {
        XCTAssertEqual(NowCard.plainEta(minutes: 200), "3 h 20 m")
        XCTAssertEqual(NowCard.plainEta(minutes: 45), "45 min")
        XCTAssertEqual(NowCard.eta(minutes: 200), "~3 h 20 m")
    }

    func testTheLiveActivityStateUsesTheSameFigure() {
        XCTAssertEqual(ChargingActivityMath.state(from: summary(truck: 360, validity: "valid"))?.minutesToTarget, 360)
        XCTAssertEqual(ChargingActivityMath.state(from: summary(truck: 360, validity: "invalid"))?.minutesToTarget, 338)
    }
}
