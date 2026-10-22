import SwiftUI
import XCTest
@testable import Rivolt

final class ChargeInsightsTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)

    private func charge(maxKw: Double = 142, energy: Double = 20, soc: (Double, Double) = (41, 80), cost: Double = 0, currency: String = "USD",
                        source: String? = nil, derived: Bool? = nil, estimated: Double? = nil, kind: String? = "dcfc", place: String? = nil,
                        network: String? = nil, minutes: Double = 40) throws -> ActivityCharge {
        func q(_ s: String?) -> String { s.map { "\"\($0)\"" } ?? "null" }
        let iso = ISO8601DateFormatter()
        let json = """
        {"id":"c1","vehicle_id":"veh-1","started_at":"\(iso.string(from: t0))","ended_at":"\(iso.string(from: t0.addingTimeInterval(minutes * 60)))",
         "energy_added_kwh":\(energy),"start_soc_pct":\(soc.0),"end_soc_pct":\(soc.1),"max_power_kw":\(maxKw),"cost":\(cost),"currency":"\(currency)",
         "cost_source_effective":\(q(source)),"cost_source_derived":\(derived.map { $0 ? "true" : "false" } ?? "null"),
         "estimated_cost":\(estimated.map { String($0) } ?? "null"),"estimated_currency":\(estimated == nil ? "null" : "\"USD\""),
         "kind":\(q(kind)),"place":\(q(place)),"network":\(q(network))}
        """
        return try JSON.decoder().decode(ActivityCharge.self, from: Data(json.utf8))
    }

    private func s(_ sec: Double, kw: Double = 0, limit: Double? = nil, out: Double? = nil, inside: Double? = nil) -> ChargeSample {
        ChargeSample(at: t0.addingTimeInterval(sec), batteryLevelPct: 50, chargerPowerKw: kw, packTempAvgC: nil, packTempMinC: nil, packTempMaxC: nil,
                     chargeLimitPct: limit, outsideTempC: out, insideTempC: inside)
    }

    func testTheChargerClassIsTheWebsByPeakPower() {
        XCTAssertEqual(ChargeInsights.tier(maxKw: 0).label, "—")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 1.4).label, "L1 (120 V)")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 11.5).label, "L2")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 19).label, "L2 high power")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 50).label, "DC slow")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 142).label, "DC standard")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 190).label, "DC fast")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 320).label, "DC ultra")
    }

    func testTheSampleNumbers() throws {
        let c = try charge(maxKw: 100, minutes: 20)
        let samples = [
            s(-90, kw: 5),                  // before the start: not counted
            s(0, kw: 20, limit: 80, out: 10, inside: 20),
            s(30, kw: 95, limit: 80, out: 12, inside: 22),   // within 90% of the peak: 30 s to peak
            s(60, kw: 100, limit: 90, out: 14, inside: 0),
            s(600, kw: 100, limit: 80),     // a 540 s gap: counted as the 300 s cap
            s(660, kw: 0, limit: 80),
            s(1_200, kw: 0),
        ]
        let i = ChargeInsights.compute(c, samples: samples)
        XCTAssertEqual(i.timeToPeakSec, 30)
        XCTAssertEqual(i.activeSec, 30 + 30 + 300 + 60, "each step after a sample above 0 kW, capped at 5 minutes")
        XCTAssertEqual(i.activePct, 420 / 1_200 * 100, accuracy: 1e-9)
        XCTAssertEqual(i.limitPct, 80, "the most common limit, not the last")
        XCTAssertEqual(i.outsideTempC ?? 0, 12, accuracy: 1e-9)
        XCTAssertEqual(i.insideTempC ?? 0, 21, accuracy: 1e-9, "a 0 reading is not a temperature")
    }

    func testARowAloneStillGivesTheRestAndTheSampleRowsAreEmpty() throws {
        let c = try charge(energy: 20, soc: (41, 80), cost: 6.4, source: "rivian", minutes: 60)
        let i = ChargeInsights.compute(c, samples: [])
        XCTAssertEqual(i.socGainedPct, 39)
        XCTAssertEqual(i.socPerHour, 39, accuracy: 1e-9)
        XCTAssertEqual(i.tier.label, "DC standard")
        XCTAssertEqual(i.costPerKwh, "$0.320/kWh")
        XCTAssertEqual(i.timeToPeakSec, 0)
        XCTAssertEqual(i.activeSec, 0)
        XCTAssertNil(i.limitPct)
        XCTAssertNil(i.outsideTempC)
        XCTAssertNil(i.insideTempC)
    }

    func testThePricePerKwhIsAnEstimateWhenTheCostIs() throws {
        XCTAssertEqual(ChargeInsights.costPerKwh(try charge(energy: 40, cost: 0, source: "home_rate", estimated: 5.2)), "~$0.130/kWh")
        XCTAssertEqual(ChargeInsights.costPerKwh(try charge(energy: 40, cost: 5.2, source: "network_estimate")), "~$0.130/kWh")
        XCTAssertNil(ChargeInsights.costPerKwh(try charge(energy: 40, cost: 0, source: "user")), "free")
        XCTAssertNil(ChargeInsights.costPerKwh(try charge(energy: 40, cost: 0, source: "unknown")))
        XCTAssertNil(ChargeInsights.costPerKwh(try charge(energy: 0, cost: 5, source: "rivian")))
    }

    func testTheSessionRowsAreInThePhonesUnitsAndSayDashWhenUnknown() throws {
        let c = try charge(maxKw: 142, energy: 20.456, soc: (41, 80), cost: 6.4, source: "rivian", minutes: 60)
        let i = ChargeInsights.compute(c, samples: [s(0, kw: 142, limit: 80, out: 10, inside: 20), s(60, kw: 142, limit: 80, out: 10, inside: 20)])
        let g = ChargeSessionRows.groups(c, i, metric: false)
        XCTAssertEqual(g.map(\.title), ["Session", "Power & timing", "Battery & energy", "Environment"])
        let rows = Dictionary(uniqueKeysWithValues: g.flatMap(\.rows).map { ($0.label, $0.value) })
        XCTAssertEqual(rows["Where"], "Fast charge")
        XCTAssertEqual(rows["Peak power"], "142.0 kW")
        XCTAssertEqual(rows["SoC gained"], "39.0% (41% → 80%)")
        XCTAssertEqual(rows["Charge limit"], "80%")
        XCTAssertEqual(rows["Energy added"], "20.46 kWh")
        XCTAssertEqual(rows["Outside temperature"], "50°F")
        XCTAssertEqual(rows["Inside temperature"], "68°F")
        let metric = ChargeSessionRows.groups(c, i, metric: true).flatMap(\.rows).first { $0.label == "Outside temperature" }
        XCTAssertEqual(metric?.value, "10°C")
        let bare = ChargeSessionRows.groups(c, ChargeInsights.compute(c, samples: []), metric: false)
        XCTAssertEqual(bare.map(\.title), ["Session", "Power & timing", "Battery & energy"], "no temperatures: no Environment group")
        let dashes = Dictionary(uniqueKeysWithValues: bare.flatMap(\.rows).map { ($0.label, $0.value) })
        XCTAssertEqual(dashes["Time to peak"], "—")
        XCTAssertEqual(dashes["Charge limit"], "—")
    }

    func testThePricingSummaryIsReadOnlyAndSaysWhereThePriceCameFrom() throws {
        let billed = ChargePricing.summary(try charge(cost: 6.4, source: "rivian", network: "Rivian Adventure Network"))
        XCTAssertEqual(billed.cost.text, "$6.40")
        XCTAssertEqual(billed.source, "Billed")
        XCTAssertNil(billed.advice)
        let est = ChargePricing.summary(try charge(cost: 0, source: "home_rate", estimated: 5.2))
        XCTAssertEqual(est.cost.tone, .estimate)
        XCTAssertNotNil(est.advice)
        let none = ChargePricing.summary(try charge(cost: 0, source: "unknown"))
        XCTAssertEqual(none.cost.text, "Unpriced")
        XCTAssertEqual(none.advice, "This charge has no price yet.")
        XCTAssertEqual(ChargePricing.source(try charge(cost: 0, source: "user")), "Free · entered by you")
        XCTAssertEqual(ChargePricing.source(try charge(cost: 3, source: "home_rate", derived: true)), "Home rate (worked out, not recorded)")
        XCTAssertEqual(MoneyLink.charge("c1", pricing: true).path, "/charges/c1#pricing")
    }

    @MainActor
    func testTheModelGivesTheRowNumbersEvenWhenTheSamplesCantBeRead() async throws {
        let c = try charge()
        let model = ChargeCurveModel()
        await model.load(source: Failing(), charge: c, now: t0.addingTimeInterval(86_400))
        XCTAssertEqual(model.phase, .failed)
        XCTAssertEqual(model.insights?.socGainedPct, 39)
        XCTAssertEqual(model.insights?.tier.label, "DC standard")
    }
}

private struct Failing: ChargeSamplesSource {
    func chargeSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [ChargeSample] { throw URLError(.timedOut) }
}
