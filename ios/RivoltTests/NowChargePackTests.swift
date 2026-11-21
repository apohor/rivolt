import XCTest
@testable import Rivolt

/// The time left in a charge needs the pack size. The live state often carries none (`battery_capacity_kwh` 0), and the web then reads the vehicle's
/// recorded `pack_kwh`; the phone did not, so the Now card lost "N min left (est.)" and the graph's estimate line, and the Live screen lost "Remaining".
@MainActor
final class NowChargePackTests: XCTestCase {
    private let at = Date(timeIntervalSince1970: 1_800_000_000)

    private func state(soc: Double, limit: Double, kw: Double, capacity: Double? = nil) throws -> VehicleState {
        let json = #"{"at":"\#(ISO8601DateFormatter().string(from: at))","vehicle_id":"v1","battery_level_pct":\#(soc),"distance_to_empty":300,"gear":"P","power_state":"ready","charger_state":"charging_active","charger_power_kw":\#(kw),"charge_target_pct":\#(limit)\#(capacity.map { #","battery_capacity_kwh":\#($0)"# } ?? "")}"#
        return try JSON.decoder().decode(VehicleState.self, from: Data(json.utf8))
    }

    private func snapshot(peak: Double, extra: String = "") throws -> NowSnapshot {
        let iso = ISO8601DateFormatter().string(from: at)
        let started = ISO8601DateFormatter().string(from: at.addingTimeInterval(-120))
        let json = #"{"version":1,"at":"\#(iso)","connection":"live","state":"charging","failed_sleeps_6h":0,"charge":{"started_at":"\#(started)","soc_start":78,"peak_kw":\#(peak)\#(extra)}}"#
        return try JSON.decoder().decode(NowSnapshot.self, from: Data(json.utf8))
    }

    private func stats(_ summary: NowSummary, _ now: NowSnapshot, _ session: LiveSession?) -> [NowSlotModel.Stat] {
        NowSlotModel.liveStats(summary: summary, snapshot: now, session: session, charge: nil, clock: at)
    }

    /// A billed Rivian session at a fast charger, as the web's Live card shows it: 81% to a limit of 84% at 123 kW.
    func testABilledRivianDCChargeWithNoPackInTheStateStillSaysHowLongIsLeft() throws {
        let raw = try state(soc: 81, limit: 84, kw: 123)
        let now = try snapshot(peak: 123)
        let session = LiveSession(active: true, timeElapsedSeconds: 120, powerKw: 123, kilometersChargedPerHour: 300, rangeAddedKm: 12,
                                  totalChargedEnergyKwh: 3.3, currentPrice: "1.91", currentCurrency: "USD", isRivianCharger: true)
        XCTAssertNil(NowSummary(state: raw, now: now, clock: at).chargeEta, "the fault: no pack, no estimate")

        let withPack = NowReads.withPack(raw, pack: 135)
        let summary = NowSummary(state: withPack, now: now, clock: at)
        XCTAssertEqual(summary.chargeEta, ChargeETA.Result(minutes: 2, source: .estimate))
        let list = stats(summary, now, session)
        XCTAssertEqual(list.map(\.label), ["Rate", "Range added", "Energy", "Elapsed", "Remaining", "Cost so far"])
        XCTAssertEqual(list.first { $0.label == "Remaining" }?.estimated, true)
        XCTAssertEqual(list.first { $0.label == "Cost so far" }?.note, "billed by Rivian")
        let slot = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: session, charge: nil, liveDrive: nil, fix: nil, home: nil, clock: at))
        XCTAssertEqual(slot.chips.map(\.text), ["Rivian charger"])
        XCTAssertEqual(NowSlotModel.chargerTag(kw: 123, snapshot: now, session: session, fix: nil, home: nil), "Rivian charger", "the Live screen's tag is the card's")
        guard case .figures(_, _, _, let small, _) = slot.content else { return XCTFail("figures") }
        XCTAssertEqual(small?.contains("(est.)"), true, "the line under the kW: \(small ?? "nil")")
    }

    /// A home AC charge: no session at all, the fix at the saved home.
    func testAHomeACChargeWorksTheTimeLeftOutFromTheVehiclesPack() throws {
        let raw = try state(soc: 60, limit: 80, kw: 11)
        let now = try snapshot(peak: 11, extra: #","cost":2.13,"currency":"USD","cost_source":"home_rate","cost_is_estimate":true"#)
        let summary = NowSummary(state: NowReads.withPack(raw, pack: 135), now: now, clock: at)
        XCTAssertEqual(summary.chargeEta?.source, .estimate)
        XCTAssertEqual(summary.chargeEta?.minutes, 148)   // 20 % of 135 kWh at 11 kW
        XCTAssertTrue(stats(summary, now, nil).contains { $0.label == "Remaining" && $0.estimated })
        let home = NowSlotModel.Home(lat: 34.0, lon: -118.0, label: nil)
        XCTAssertEqual(NowSlotModel.chargerTag(kw: 11, snapshot: now, session: nil, fix: (34.0, -118.0), home: home), "Home charger")
    }

    /// A session that reports only a price and the power (the rest worked out from the battery).
    func testASessionWithOnlyAPriceAndPowerStillGetsARemainingRowAndNoRivianTag() throws {
        let raw = try state(soc: 50, limit: 80, kw: 60)
        let now = try snapshot(peak: 60)
        let session = LiveSession(active: true, powerKw: 60, currentPrice: "2.27", currentCurrency: "USD")
        let summary = NowSummary(state: NowReads.withPack(raw, pack: 100), now: now, clock: at)
        let list = stats(summary, now, session)
        XCTAssertEqual(list.first { $0.label == "Remaining" }?.estimated, true)
        XCTAssertEqual(NowSlotModel.chargerTag(kw: 60, snapshot: now, session: session, fix: nil, home: nil), "Fast charger", "no Rivian flag: the peak decides")
    }

    func testThePacksOwnFigureIsKeptAndAZeroPackIsNotUsed() throws {
        XCTAssertEqual(NowReads.withPack(try state(soc: 50, limit: 80, kw: 60, capacity: 131), pack: 135)?.batteryCapacityKwh, 131)
        XCTAssertEqual(NowReads.withPack(try state(soc: 50, limit: 80, kw: 60, capacity: 0), pack: 135)?.batteryCapacityKwh, 135)
        XCTAssertNil(NowReads.withPack(try state(soc: 50, limit: 80, kw: 60), pack: 0)?.batteryCapacityKwh)
        XCTAssertNil(NowReads.withPack(nil, pack: 135))
    }

    func testAVehicleReadsItsPackSize() throws {
        let json = #"{"id":"v1","vin":"X","name":"R1S","model":"R1S","pack_kwh":135.5}"#
        XCTAssertEqual(try JSON.decoder().decode(Vehicle.self, from: Data(json.utf8)).packKwh, 135.5)
    }
}
