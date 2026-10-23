import XCTest
@testable import Rivolt

/// The trailer tag on drives: decoded as the server's effective value, left out of the efficiency figures, said in the
/// web's words.
final class TrailerTests: XCTestCase {
    private func drive(miles: Double = 20, energy: Double = 10, trailer: Bool? = nil) -> ActivityDrive {
        ActivityDrive(id: UUID().uuidString, vehicleId: "v", startedAt: .now, endedAt: .now, distanceMi: miles, energyUsedKwh: energy,
                      startSocPct: 80, endSocPct: 70, avgSpeedMph: 40, maxSpeedMph: 66, estimatedCost: nil, estimatedCurrency: nil,
                      estimatedPricePerKwh: nil, roundTripLegs: nil, drivingSeconds: nil, trailer: trailer)
    }

    func testTheEffectiveTrailerDecodesAndAnAbsentOneIsUnknown() throws {
        let base = #"{"id":"d","vehicle_id":"v","started_at":"2026-10-20T10:00:00Z","ended_at":"2026-10-20T10:20:00Z","distance_mi":5,"energy_used_kwh":2,"start_soc_pct":80,"end_soc_pct":75,"avg_speed_mph":30,"max_speed_mph":60"#
        let with = try JSON.decoder().decode(ActivityDrive.self, from: Data((base + #","trailer":true,"trailer_override":null}"#).utf8))
        XCTAssertEqual(with.trailer, true)
        XCTAssertTrue(with.hasTrailer)
        let none = try JSON.decoder().decode(ActivityDrive.self, from: Data((base + #","trailer":null}"#).utf8))
        XCTAssertNil(none.trailer)
        XCTAssertFalse(none.hasTrailer)
        let old = try JSON.decoder().decode(ActivityDrive.self, from: Data((base + "}").utf8))
        XCTAssertFalse(old.hasTrailer, "a server that predates the field")
    }

    func testTrailerDrivesAreLeftOutOfTheEfficiencyFiguresButNotTheCounts() {
        let d = KpiLogic.drives([drive(), drive(miles: 20, energy: 20, trailer: true), drive(trailer: false)])
        XCTAssertEqual(d.count, 3)
        XCTAssertEqual(d.miles, 60, accuracy: 1e-9)
        XCTAssertEqual(d.trailerExcluded, 1)
        XCTAssertEqual(d.energyUsedKwh, 20, accuracy: 1e-9)
        XCTAssertEqual(d.milesForEnergy, 40, accuracy: 1e-9)
        XCTAssertEqual(d.miPerKwh, 2, accuracy: 1e-9)
        XCTAssertEqual(d.milesPerPct, 2, accuracy: 1e-9, "40 plain miles over 20 plain percent")
        XCTAssertEqual(d.trailerNote, "excludes 1 drive with a trailer")
        XCTAssertEqual(KpiLogic.drives([drive(trailer: true), drive(trailer: true)]).trailerNote, "excludes 2 drives with a trailer")
        XCTAssertNil(KpiLogic.drives([drive()]).trailerNote)
    }

    func testTheEfficiencyTileSaysHowManyWereLeftOut() {
        let tile = KpiLogic.tiles(drives: KpiLogic.drives([drive(), drive(trailer: true)]), charges: KpiLogic.charges([]), since: nil,
                                  sinceKnown: true, ledger: nil, ledgerPending: false, metric: false).first { $0.id == "efficiency" }
        XCTAssertEqual(tile?.hint, "pack-side · 20 mi / 10 kWh · excludes 1 drive with a trailer")
    }

    func testTheWallToWheelFigureIncludesTrailerDrivesAndSaysSo() {
        func hint(_ ds: [ActivityDrive]) -> String? {
            KpiLogic.tiles(drives: KpiLogic.drives(ds), charges: KpiLogic.charges([charge()]), since: nil, sinceKnown: true, ledger: nil,
                           ledgerPending: false, metric: false).first { $0.id == "efficiency" }?.hint
        }
        // No drive has energy recorded, so the figure is all miles over the energy charged, trailer drives included.
        XCTAssertEqual(hint([drive(energy: 0), drive(energy: 0, trailer: true)]), "wall-to-wheel · 40 mi / 25 kWh · includes 1 drive with a trailer")
        XCTAssertEqual(hint([drive(energy: 0)]), "wall-to-wheel · 20 mi / 25 kWh")
    }

    func testNoFigureHasNothingToNote() {
        let tile = KpiLogic.tiles(drives: KpiLogic.drives([drive(energy: 0, trailer: true)].map { ActivityDrive(id: $0.id, vehicleId: "v", startedAt: .now, endedAt: .now, distanceMi: 0, energyUsedKwh: 0, startSocPct: 70, endSocPct: 70, avgSpeedMph: 0, maxSpeedMph: 66, estimatedCost: nil, estimatedCurrency: nil, estimatedPricePerKwh: nil, roundTripLegs: nil, drivingSeconds: nil, trailer: true) }),
                                  charges: KpiLogic.charges([]), since: nil, sinceKnown: true, ledger: nil, ledgerPending: false, metric: false).first { $0.id == "efficiency" }
        XCTAssertEqual(tile?.value, "—")
        XCTAssertEqual(tile?.hint, "top speed 66 mph")
    }

    private func charge() -> ActivityCharge {
        ActivityCharge(id: "c", vehicleId: "v", startedAt: .now, endedAt: .now, energyAddedKwh: 25, startSocPct: 40, endSocPct: 60, maxPowerKw: 11,
                       cost: 0, currency: "USD", costSourceEffective: nil, costSourceDerived: nil, estimatedCost: nil, estimatedCurrency: nil,
                       kind: nil, place: nil, network: nil)
    }

    func testTheTrendAndEfficiencyNotesUseTheWebsWords() throws {
        let trends = try JSON.decoder().decode(TruckTrends.self, from: Data(#"""
        {"distance_unit":"mi","efficiency_unit":"mi/kWh","min_month_distance":50,"min_months":3,"qualifying_months":3,
         "efficiency":[{"month":"2026-10","distance":300,"efficiency":2.5,"trailer":{"drives":2,"distance":120,"energy_kwh":80,"efficiency":1.5}}],
         "excluded_trailer_drives":2,"capacity":null}
        """#.utf8))
        XCTAssertEqual(TruckLogic.trailerTrendNote(trends), "Excludes 2 drives with a trailer, which ran 1.50 mi/kWh over 120 mi.")
        let none = try JSON.decoder().decode(TruckTrends.self, from: Data(#"""
        {"distance_unit":"mi","efficiency_unit":"mi/kWh","min_month_distance":50,"min_months":3,"qualifying_months":0,"efficiency":[],"capacity":null}
        """#.utf8))
        XCTAssertNil(TruckLogic.trailerTrendNote(none))
    }
}
