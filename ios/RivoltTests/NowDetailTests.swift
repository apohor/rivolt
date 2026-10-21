import XCTest
@testable import Rivolt

final class NowDetailTests: XCTestCase {
    private func charging() throws -> NowSummary {
        let st = try Fixture.decode(VehicleState.self, "state")
        let now = try Fixture.decode(NowSnapshot.self, "now_v1")
        return NowSummary(state: st, now: now, clock: try XCTUnwrap(now.at).addingTimeInterval(60))
    }

    private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
        try JSON.decoder().decode(T.self, from: Data(json.utf8))
    }

    func testLiveSessionDecodesTheServersKeysAndFillsGaps() throws {
        let s = try decode(LiveSession.self, #"{"active":true,"time_elapsed_seconds":1800,"time_remaining_seconds":2400,"power_kw":11.1,"kilometers_charged_per_hour":58,"range_added_km":29,"total_charged_energy_kwh":5.6,"soc_pct":63,"current_price":"","current_currency":"","is_free_session":false,"is_rivian_charger":false,"estimated_cost":0.9,"estimated_currency":"USD"}"#)
        XCTAssertEqual(s.timeRemainingSeconds, 2400)
        XCTAssertEqual(s.estimatedCost, 0.9)
        let empty = try decode(LiveSession.self, "{}")
        XCTAssertFalse(empty.active)
        XCTAssertEqual(empty.currentPrice, "")
        XCTAssertNil(empty.estimatedCost)
    }

    func testLiveDriveAndTheListRowsDecodeOnlyTheirFigures() throws {
        let d = try decode(LiveDrive.self, #"{"elapsed_sec":600,"distance_mi":7.5,"max_speed_mph":70,"avg_speed_mph":45,"energy_used_kwh":2.5,"mi_per_kwh":3.0,"start_soc_pct":80}"#)
        XCTAssertEqual(d.distanceMi, 7.5)
        let charges = try decode([ChargeBrief].self, #"[{"ID":"c1","StartedAt":"2026-10-21T12:40:00Z","EndedAt":"2026-10-21T13:40:00.123456789Z","EnergyAddedKWh":20.5,"Lat":30.1,"Lon":-97.7,"Cost":4.1,"Currency":"USD","kind":"ac","place":"home","estimated_cost":4.1}]"#)
        XCTAssertEqual(charges[0].placeLabel, "Home")
        XCTAssertEqual(charges[0].energyAddedKwh, 20.5)
        let drives = try decode([DriveBrief].self, #"[{"ID":"d1","StartedAt":"2026-10-21T10:00:00Z","EndedAt":"2026-10-21T10:30:00Z","DistanceMi":12,"AvgSpeedMph":40,"EnergyUsedKWh":4,"StartLat":30.1,"RoutePolyline":"abc"}]"#)
        XCTAssertEqual(drives[0].miPerKwh, 3)
    }

    func testPlaceLabelFollowsTheWebsRules() throws {
        func label(_ kind: String?, _ place: String?) throws -> String? {
            let json = "[{\"ID\":\"c\"" + (kind.map { ",\"kind\":\"\($0)\"" } ?? "") + (place.map { ",\"place\":\"\($0)\"" } ?? "") + "}]"
            return try decode([ChargeBrief].self, json)[0].placeLabel
        }
        XCTAssertEqual(try label("dcfc", "away"), "Fast")
        XCTAssertEqual(try label("ac", "away"), "Away")
        XCTAssertNil(try label("ac", "unknown"))
        XCTAssertNil(try label(nil, nil))
    }

    func testAChargeInProgressShowsPowerEtaAndAnEstimatedCostMarkedAsOne() throws {
        let session = LiveSession(active: true, timeElapsedSeconds: 1800, timeRemainingSeconds: 2400, powerKw: 11.2, kilometersChargedPerHour: 58,
                                  rangeAddedKm: 29, totalChargedEnergyKwh: 5.6, socPct: 63.4, estimatedCost: 0.9, estimatedCurrency: "USD")
        let d = try XCTUnwrap(ChargingDetail.make(summary: try charging(), session: session, charge: nil))
        XCTAssertEqual(d.powerKw, 11.2)
        XCTAssertEqual(d.remaining, 2400)
        XCTAssertEqual(d.toTargetPct ?? 0, 16.6, accuracy: 0.01)
        XCTAssertEqual(d.cost, .estimate(amount: 0.9, currency: "USD"))
        XCTAssertTrue(NowFormat.costText(try XCTUnwrap(d.cost)).hasSuffix("(estimate)"))
        XCTAssertTrue(d.haveLiveData)
    }

    func testRivianBilledFreeAndNoPrice() throws {
        let sum = try charging()
        XCTAssertEqual(ChargingDetail.make(summary: sum, session: LiveSession(active: true, currentPrice: "12.34", currentCurrency: "USD"), charge: nil)?.cost, .billed(amount: 12.34, currency: "USD"))
        XCTAssertEqual(ChargingDetail.make(summary: sum, session: LiveSession(active: true, isFreeSession: true), charge: nil)?.cost, .free)
        XCTAssertNil(ChargingDetail.make(summary: sum, session: LiveSession(active: true), charge: nil)?.cost)
        // Nothing from Rivian's feed yet (a home charge): the basics only.
        let bare = try XCTUnwrap(ChargingDetail.make(summary: sum, session: LiveSession(), charge: nil))
        XCTAssertFalse(bare.haveLiveData)
        XCTAssertEqual(bare.powerKw, 11.1, "falls back to the state's power")
    }

    func testNoChargeCardUnlessCharging() throws {
        let parked = NowSummary(input: .init(socPct: 80, rangeKm: 300, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_disconnected", chargerPowerKw: 0, at: Date()), now: nil, clock: Date())
        XCTAssertNil(ChargingDetail.make(summary: parked, session: LiveSession(active: true), charge: nil))
    }

    func testTheDriveCardShowsTheLiveDriveInGearThenTheLastOne() throws {
        let live = LiveDrive(elapsedSec: 600, distanceMi: 7.5, maxSpeedMph: 70, avgSpeedMph: 45, energyUsedKwh: 2.5, miPerKwh: 3)
        let driving = NowSummary(input: .init(socPct: 70, rangeKm: 250, chargeTargetPct: nil, gear: "D", powerState: "go", chargerState: nil, chargerPowerKw: nil, at: Date()), now: nil, clock: Date())
        let now = try XCTUnwrap(DriveDetail.make(summary: driving, live: live, last: nil))
        XCTAssertEqual(now.kind, .now)
        XCTAssertEqual(now.distanceMi, 7.5)
        let parked = NowSummary(input: .init(socPct: 70, rangeKm: 250, chargeTargetPct: nil, gear: "P", powerState: "ready", chargerState: nil, chargerPowerKw: nil, at: Date()), now: nil, clock: Date())
        let lastRow = try decode([DriveBrief].self, #"[{"ID":"d1","StartedAt":"2026-10-21T10:00:00Z","EndedAt":"2026-10-21T10:30:00Z","DistanceMi":12,"AvgSpeedMph":40,"EnergyUsedKWh":4}]"#)[0]
        let last = try XCTUnwrap(DriveDetail.make(summary: parked, live: live, last: lastRow))
        if case .last = last.kind {} else { XCTFail("expected the last drive") }
        XCTAssertEqual(last.elapsed, 1800)
        XCTAssertNil(DriveDetail.make(summary: parked, live: nil, last: nil))
    }

    func testFormatting() {
        XCTAssertEqual(NowFormat.duration(2400), "40 min")
        XCTAssertEqual(NowFormat.duration(4320), "1 h 12 min")
        XCTAssertEqual(NowFormat.money(4.5, currency: ""), "4.50")
    }
}
