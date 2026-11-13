import SwiftUI
import XCTest
@testable import Rivolt

/// The iPhone's Now v2 (design_preview now_v2): the truck picture rule, the status chip's words and the month strip's figures, as the web's.
@MainActor
final class NowV2Tests: XCTestCase {
    private func img(_ url: String, _ placement: String, _ design: String = "dark", _ size: String = "large") -> VehicleImage {
        VehicleImage(url: url, placement: placement, design: design, size: size)
    }

    func testVehicleDecodesImages() throws {
        let json = #"{"id":"v","vin":"X","name":"T","model":"R1S","images":[{"url":"https://img.example/a.png","placement":"three-quarter","design":"light","size":"large"}]}"#
        let v = try JSON.decoder().decode(Vehicle.self, from: Data(json.utf8))
        XCTAssertEqual(v.images?.first?.placement, "three-quarter")
        XCTAssertNil(try Fixture.decode([Vehicle].self, "vehicles")[0].images)
    }

    func testDefaultIsThreeQuarterInThePagesDesign() {
        let images = [img("https://i.example/side", "side"), img("https://i.example/tq-dark", "three-quarter"), img("https://i.example/tq-light", "three-quarter", "light")]
        XCTAssertEqual(VehiclePicture.forNow(images, kind: .parked, light: false)?.absoluteString, "https://i.example/tq-dark")
        XCTAssertEqual(VehiclePicture.forNow(images, kind: .parked, light: true)?.absoluteString, "https://i.example/tq-light")
    }

    func testChargingAndDrivingWantTheirPlacement() {
        let images = [img("https://i.example/tq", "three-quarter"), img("https://i.example/side", "side"), img("https://i.example/chg", "side-charging")]
        XCTAssertEqual(VehiclePicture.forNow(images, kind: .charging, light: false)?.absoluteString, "https://i.example/chg")
        XCTAssertEqual(VehiclePicture.forNow(images, kind: .driving, light: false)?.absoluteString, "https://i.example/side")
    }

    func testMissingPlacementFallsBackAndInteriorIsNeverShown() {
        XCTAssertEqual(VehiclePicture.forNow([img("https://i.example/front", "front")], kind: .charging, light: false)?.absoluteString, "https://i.example/front")
        XCTAssertNil(VehiclePicture.forNow([img("https://i.example/in", "interior-front")], kind: .parked, light: false))
        XCTAssertNil(VehiclePicture.forNow(nil, kind: .parked, light: false))
        XCTAssertNil(VehiclePicture.forNow([img("http://i.example/plain", "side")], kind: .parked, light: false), "https only")
    }

    func testChipNeverSaysLive() throws {
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let kinds: [(NowSummary.Input, TimeInterval)] = [
            (.init(socPct: 72, rangeKm: 330, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_disconnected", chargerPowerKw: 0, at: at), 15),
            (.init(socPct: 72, rangeKm: 330, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_disconnected", chargerPowerKw: 0, at: at), 600),
            (.init(socPct: 72, rangeKm: 330, chargeTargetPct: 80, gear: "P", powerState: "sleep", chargerState: "charger_disconnected", chargerPowerKw: 0, at: at), 15),
            (.init(socPct: 72, rangeKm: 330, chargeTargetPct: 80, gear: "D", powerState: "go", chargerState: "charger_disconnected", chargerPowerKw: 0, at: at), 15),
            (.init(socPct: 72, rangeKm: 330, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: "charger_connected", chargerPowerKw: 0, at: at), 15),
        ]
        for (input, age) in kinds {
            let clock = at.addingTimeInterval(age)
            let text = NowChip.text(for: NowSummary(input: input, now: nil, clock: clock), clock: clock)
            XCTAssertFalse(text.isEmpty)
            XCTAssertFalse(text.contains("Live"), "'\(text)' must not say Live")
        }
    }

    func testChipNamesChargingWithPower() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let clock = try XCTUnwrap(now.at).addingTimeInterval(120)
        let text = NowChip.text(for: NowSummary(state: st, now: now, clock: clock), clock: clock)
        XCTAssertTrue(text.hasPrefix("Charging"), text)
        XCTAssertFalse(text.contains("Live"))
    }

    func testMonthStripFiguresFollowUnits() {
        let tiles = [KpiTile(id: "miles", label: "Miles", value: "812", unit: "mi", hint: nil), KpiTile(id: "energy", label: "Energy", value: "240", unit: "kWh", hint: nil),
                     KpiTile(id: "efficiency", label: "Efficiency", value: "2.6", unit: "mi/kWh", hint: nil), KpiTile(id: "cost", label: "Cost", value: "$61", unit: "", hint: nil)]
        XCTAssertEqual(NowMonthStripFigure.make(tiles: tiles, metric: false).map(\.label), ["miles", "kWh added", "mi/kWh", "spent"])
        XCTAssertEqual(NowMonthStripFigure.make(tiles: tiles, metric: true).map(\.label), ["km", "kWh added", "km/kWh", "spent"])
        XCTAssertEqual(NowMonthStripFigure.make(tiles: tiles, metric: false).map(\.value), ["812", "240", "2.6", "$61"])
        XCTAssertEqual(NowMonthStripFigure.make(tiles: [], metric: false).map(\.value), ["—", "—", "—", "—"])
    }

    func testRenderV2() throws {
        let (st, now) = (try Fixture.decode(VehicleState.self, "state"), try Fixture.decode(NowSnapshot.self, "now_v1"))
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let clock = try XCTUnwrap(now.at).addingTimeInterval(120)
        let summary = NowSummary(state: st, now: now, clock: clock)
        for scheme in [ColorScheme.light, .dark] {
            let card = NowCard(vehicle: vehicle, summary: summary, clock: clock, v2: true, fullChargeRange: 283)
                .padding().frame(width: 393).background(Color(.systemGroupedBackground)).environment(\.colorScheme, scheme)
            let r = ImageRenderer(content: card)
            r.scale = 2
            let image = try XCTUnwrap(r.uiImage)
            XCTAssertGreaterThan(image.size.height, 200)
        }
    }

    func testSublineChargingAndParked() throws {
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        func card(_ charger: String, range: Int?) -> NowCard {
            let input = NowSummary.Input(socPct: 72, rangeKm: 330, chargeTargetPct: 80, gear: "P", powerState: "ready", chargerState: charger, chargerPowerKw: charger == "charging_active" ? 11 : 0, at: at)
            return NowCard(vehicle: vehicle, summary: NowSummary(input: input, now: nil, clock: at.addingTimeInterval(10)), clock: at.addingTimeInterval(10), v2: true, fullChargeRange: range)
        }
        XCTAssertEqual(card("charger_disconnected", range: 283).subline.map { $0.hasPrefix("Full charge today ≈ 283 ") }, true)
        XCTAssertNil(card("charger_disconnected", range: nil).subline)
        let line = try XCTUnwrap(card("charging_active", range: 283).subline)
        XCTAssertTrue(line.hasPrefix("Charging to 80% · ≈ "), line)
        XCTAssertTrue(line.hasSuffix(" when done"), line)
    }

    func testColdRangeDecodesAndNeedsBothEnds() throws {
        let r = try Fixture.decode(ColdRange.self, "cold_range")
        XCTAssertEqual(r.fullChargeRange, 283)
    }
}
