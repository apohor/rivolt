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
        XCTAssertEqual(ChargeInsights.tier(maxKw: 1.4).label, "Household outlet (Level 1)")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 11.5).label, "Level 2")
        XCTAssertEqual(ChargeInsights.tier(maxKw: 19).label, "Level 2, high power")
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
        XCTAssertEqual(ChargeInsights.costPerKwh(c), "$0.320/kWh")
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
        XCTAssertEqual(rows["Type"], "Fast charge")
        XCTAssertNil(rows["Price per kWh"], "the price per kWh is on the Pricing card only")
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
    func testTheCardKeepsTheRowNumbersWhenTheSamplesCantBeRead() async throws {
        let c = try charge()
        let model = ChargeCurveModel()
        await model.load(source: Failing(), charge: c, now: t0.addingTimeInterval(86_400))
        XCTAssertEqual(model.phase, .failed)
        XCTAssertTrue(model.samples.isEmpty)
        // Without samples the card still has the row's own numbers.
        let i = ChargeInsights.compute(c, samples: model.samples)
        XCTAssertEqual(i.socGainedPct, 39)
        XCTAssertEqual(i.tier.label, "DC standard")
    }

    // MARK: The charge's row (GET /api/charges/{id}, backend's fixtures)

    func testTheChargeRowDecodesTheTwelveFieldsAndNothingPositionalBeyondTheMaps() throws {
        let row = try Fixture.decode(ChargeRow.self, "charge_one")
        XCTAssertEqual(row.milesAdded, 214.5)
        XCTAssertEqual(row.avgPowerKw, 118.6)
        XCTAssertEqual(row.maxPowerKW, 187.2)
        XCTAssertEqual(row.finalState, "charging_complete")
        XCTAssertEqual(row.source, "live")
        XCTAssertEqual(row.thermalKwh, 3.2)
        XCTAssertEqual(row.activeSeconds, 2460)
        XCTAssertEqual(row.network, "Rivian Adventure Network")
        XCTAssertEqual(Mirror(reflecting: row).children.compactMap(\.label),
                       ["lat", "lon", "maxPowerKW", "network", "milesAdded", "avgPowerKw", "finalState", "source", "thermalKwh", "activeSeconds",
                        "placeOverride", "locationOverride"])
        // The user's own word from the web (#906): set by them, and the pin where the truck was.
        XCTAssertTrue(row.placeSetByYou)
        XCTAssertEqual(row.placeByYou, "Away")
        XCTAssertTrue(row.pinnedWhereRecorded)
        XCTAssertTrue(row.location().recorded, "the map gets the pin choice")
        let ac = try Fixture.decode(ChargeRow.self, "charge_one_ac")
        XCTAssertNil(ac.thermalKwh, "null: no thermal figure")
        XCTAssertFalse(ac.placeSetByYou, "empty: automatic")
        XCTAssertFalse(ac.pinnedWhereRecorded)
        XCTAssertFalse(ac.location().recorded)
        XCTAssertEqual(ac.activeSeconds, 19_200)
        XCTAssertEqual(row.location().network, "Rivian Adventure Network")
        XCTAssertEqual(row.location(network: "Electrify America").network, "Electrify America", "the ledger's network wins")
    }

    func testTheOverridesReadAsAutomaticFromAnOlderServerOrAnUnknownValue() throws {
        let older = try JSON.decoder().decode(ChargeRow.self, from: Data(#"{"Lat":1,"Lon":2}"#.utf8))
        XCTAssertNil(older.placeOverride)
        XCTAssertFalse(older.placeSetByYou)
        XCTAssertFalse(older.pinnedWhereRecorded)
        let odd = try JSON.decoder().decode(ChargeRow.self, from: Data(#"{"Lat":1,"Lon":2,"PlaceOverride":"garage","LocationOverride":"snapped"}"#.utf8))
        XCTAssertFalse(odd.placeSetByYou)
        XCTAssertFalse(odd.pinnedWhereRecorded)
        XCTAssertFalse(odd.location().recorded)
    }

    func testTheTypeRowSaysSetByYouOnlyWhenTheUserSetThePlace() throws {
        let c = try charge(maxKw: 11, energy: 40, soc: (40, 80), cost: 6.4, source: "home_rate", minutes: 240)
        let mine = try JSON.decoder().decode(ChargeRow.self, from: Data(#"{"Lat":1,"Lon":2,"PlaceOverride":"home"}"#.utf8))
        let auto = try JSON.decoder().decode(ChargeRow.self, from: Data(#"{"Lat":1,"Lon":2,"PlaceOverride":""}"#.utf8))
        func typeRow(_ row: ChargeRow?) -> InsightRow? {
            ChargeSessionRows.groups(c, ChargeInsights.compute(c, samples: [], row: row), metric: false).flatMap(\.rows).first { $0.label == "Type" }
        }
        // The pill says what was chosen: the Type value ("Fast charge", "Home") is the ledger's kind, not the choice.
        XCTAssertEqual(typeRow(mine)?.tag, "Home · set by you")
        let away = try JSON.decoder().decode(ChargeRow.self, from: Data(#"{"Lat":1,"Lon":2,"PlaceOverride":"away"}"#.utf8))
        XCTAssertEqual(typeRow(away)?.tag, "Away · set by you")
        XCTAssertNil(typeRow(auto)?.tag)
        XCTAssertNil(typeRow(nil)?.tag, "before the row is read")
    }

    func testTheRowAddsTheWebsRowsInThePhonesUnits() throws {
        let row = try Fixture.decode(ChargeRow.self, "charge_one")
        let c = try charge(maxKw: 187.2, energy: 81.4, soc: (18, 80), cost: 36.63, source: "rivian", network: "Rivian Adventure Network", minutes: 47)
        let i = ChargeInsights.compute(c, samples: [], row: row)
        XCTAssertEqual(i.activeSec, 2460, "the row's ActiveSeconds when there are no samples to count")
        XCTAssertEqual(i.milesAdded, 214.5)
        XCTAssertEqual(i.miPerKwh ?? 0, 214.5 / 81.4, accuracy: 1e-9)
        XCTAssertEqual(i.costPerMile, "$0.171/mi")
        XCTAssertEqual(ChargeInsights.costPerMile(c, miles: 214.5, metric: true), "$0.106/km")
        XCTAssertEqual(i.finalState, "Charging complete")
        XCTAssertEqual(i.source, "Recorded live")
        XCTAssertEqual(i.thermalKwh, 3.2)
        XCTAssertEqual(i.thermalSharePct ?? 0, 3.2 / 81.4 * 100, accuracy: 1e-9)
        XCTAssertEqual(ChargeInsights.thermalIntensity(i.thermalSharePct), "Moderate")
        let rows = Dictionary(uniqueKeysWithValues: ChargeSessionRows.groups(c, i, metric: false).flatMap(\.rows).map { ($0.label, $0) })
        XCTAssertEqual(rows["Average power"]?.value, "118.6 kW")
        XCTAssertEqual(rows["Final state"]?.value, "Charging complete")
        XCTAssertEqual(rows["Source"]?.value, "Recorded live")
        XCTAssertEqual(rows["Range efficiency"]?.value, "2.64 mi/kWh")
        XCTAssertEqual(rows["Price per mile"]?.value, "$0.171/mi")
        XCTAssertEqual(rows["Type"]?.hint, "Rivian Adventure Network", "the network under the type")
        XCTAssertEqual(rows["BMS energy"]?.value, "3.20 kWh")
        XCTAssertEqual(rows["Conditioning intensity"]?.value, "Moderate")
        // A home charge: no thermal group.
        let ac = try charge(maxKw: 11.5, energy: 58.1, soc: (42, 86), cost: 7.55, source: "home_rate", kind: "ac", place: "home", minutes: 420)
        let acInsights = ChargeInsights.compute(ac, samples: [], row: try Fixture.decode(ChargeRow.self, "charge_one_ac"))
        let titles = ChargeSessionRows.groups(ac, acInsights, metric: false).map(\.title)
        XCTAssertFalse(titles.contains("Thermal management"))
        XCTAssertEqual(acInsights.activeSec, 19_200)
    }

    func testAnEstimatedPriceReadsAsOneInEveryRow() throws {
        let c = try charge(energy: 40, cost: 0, source: "home_rate", estimated: 6.0)
        XCTAssertEqual(ChargeInsights.costPerMile(c, miles: 100, metric: false), "~$0.060/mi")
        XCTAssertNil(ChargeInsights.costPerMile(try charge(cost: 0, source: "unknown"), miles: 100, metric: false))
        XCTAssertNil(ChargeInsights.costPerMile(try charge(cost: 5, source: "rivian"), miles: 0, metric: false))
    }

    func testTheEndOfASessionAndItsSourceInTheWebsWords() {
        XCTAssertNil(ChargeInsights.finalState(nil))
        XCTAssertNil(ChargeInsights.finalState("Charging"), "the last snapshot still charging says nothing about the end")
        XCTAssertEqual(ChargeInsights.finalState("NoPower"), "No power")
        XCTAssertEqual(ChargeInsights.finalState("charging_station_err"), "Interrupted")
        XCTAssertEqual(ChargeInsights.finalState("charging_complete"), "Charging complete")
        XCTAssertEqual(ChargeInsights.source("electrafi_import"), "Imported from ElectraFi")
        XCTAssertNil(ChargeInsights.source(""))
        XCTAssertEqual(ChargeInsights.thermalIntensity(0.4), "Minimal")
        XCTAssertEqual(ChargeInsights.thermalIntensity(2), "Light")
        XCTAssertEqual(ChargeInsights.thermalIntensity(7), "Heavy")
        XCTAssertEqual(ChargeInsights.thermalIntensity(12), "Aggressive")
        XCTAssertEqual(ChargeInsights.thermalIntensity(nil), "—")
    }

    @MainActor
    func testTheRowIsReadOnceAndAFailureIsSaidOnce() async throws {
        let ok = CountingRows(result: .success(try Fixture.decode(ChargeRow.self, "charge_one")))
        let model = ChargeRowModel()
        await model.load(source: ok, id: "chg-fixture-dcfc")
        XCTAssertEqual(ok.ids, ["chg-fixture-dcfc"], "one read for the map and the session card together")
        XCTAssertEqual(model.phase.row?.milesAdded, 214.5)
        let bad = ChargeRowModel()
        await bad.load(source: CountingRows(result: .failure(URLError(.timedOut))), id: "x")
        XCTAssertEqual(bad.phase, .failed)
        XCTAssertNil(bad.phase.row)
    }
}

private final class CountingRows: ChargeRowSource, @unchecked Sendable {
    private let result: Result<ChargeRow, Error>
    private let lock = NSLock()
    private var _ids: [String] = []
    var ids: [String] { lock.withLock { _ids } }
    init(result: Result<ChargeRow, Error>) { self.result = result }
    func chargeRow(id: String) async throws -> ChargeRow {
        record(id)
        return try result.get()
    }
    private func record(_ id: String) { lock.withLock { _ids.append(id) } }
}

private struct Failing: ChargeSamplesSource {
    func chargeSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [ChargeSample] { throw URLError(.timedOut) }
}