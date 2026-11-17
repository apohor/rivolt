import SwiftUI
import XCTest
@testable import Rivolt

/// The state slot of the iPhone's Now hero (design_preview key now_slot): what each state shows, and the weather's words, as the web's.
@MainActor
final class NowSlotTests: XCTestCase {
    private let at = Date(timeIntervalSince1970: 1_800_000_000)

    private func snapshot(_ state: String, extra: String = "") throws -> NowSnapshot {
        let iso = ISO8601DateFormatter().string(from: at)
        let json = #"{"version":1,"at":"\#(iso)","connection":"live","state":"\#(state)","failed_sleeps_6h":0\#(extra)}"#
        return try JSON.decoder().decode(NowSnapshot.self, from: Data(json.utf8))
    }

    private func input(gear: String, charger: String, kw: Double, kph: Double? = nil) -> NowSummary.Input {
        .init(socPct: 60, rangeKm: 250, chargeTargetPct: 80, gear: gear, powerState: "ready", chargerState: charger, chargerPowerKw: kw, at: at, packKwh: 100, speedKph: kph)
    }

    func testSnapshotDecodesTheNewFields() throws {
        let now = try snapshot("parked", extra: #","ambient":{"temp_c":21.5,"source":"open-meteo","as_of":"2026-11-17T12:00:00Z","condition":"rain","wind_kph":16,"rain_next3h_mm":3},"location":{"lat":34.1613,"lon":-118.1676}"#)
        XCTAssertEqual(now.ambient?.condition, "rain")
        XCTAssertEqual(now.ambient?.windKph, 16)
        XCTAssertEqual(now.ambient?.rainNext3hMm, 3)
        XCTAssertEqual(now.location, NowSnapshot.Location(lat: 34.1613, lon: -118.1676))
        let driving = try snapshot("driving", extra: #","drive":{"started_at":"2026-11-17T12:00:00Z","distance_mi":12.4,"duration_s":1500,"energy_kwh":4.1,"efficiency_mi_per_kwh":3.02,"avg_speed_mph":30}"#)
        XCTAssertEqual(driving.drive?.distanceMi, 12.4)
        XCTAssertEqual(driving.drive?.efficiencyMiPerKwh, 3.02)
        XCTAssertNil(try snapshot("parked").location, "older servers and real accounts send none")
    }

    func testParkedNeedsAFixAndShowsTheWeather() throws {
        let now = try snapshot("parked", extra: #","ambient":{"temp_c":20,"source":"open-meteo","as_of":"2026-11-17T12:00:00Z","condition":"clear","wind_kph":16,"rain_next3h_mm":0}"#)
        let summary = NowSummary(input: input(gear: "P", charger: "charger_disconnected", kw: 0), now: now, clock: at)
        XCTAssertNil(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, placeLabel: nil, clock: at), "no fix, the picture stays")
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: (34.1, -118.1), placeLabel: "Home", clock: at))
        guard case .parked(_, _, let label, let weather?) = model.content else { return XCTFail("parked") }
        XCTAssertEqual(label, "Home")
        XCTAssertEqual(weather.precip, "No rain next 3 h")
        XCTAssertTrue(model.isParked)
    }

    func testWeatherWording() throws {
        func view(_ condition: String, _ mm: Double?, day: Bool = true) throws -> NowSlotModel.Weather {
            let mmJSON = mm.map { #","rain_next3h_mm":\#($0)"# } ?? ""
            let now = try snapshot("parked", extra: #","ambient":{"temp_c":10,"source":"s","as_of":"2026-11-17T12:00:00Z","condition":"\#(condition)"\#(mmJSON)}"#)
            return NowSlotModel.weatherView(try XCTUnwrap(now.ambient), day: day)
        }
        DisplayUnits.demo = true // miles
        defer { DisplayUnits.demo = false }
        XCTAssertEqual(try view("clear", 0).condition, "Sunny here now")
        XCTAssertEqual(try view("clear", 0, day: false).condition, "Clear here now")
        XCTAssertEqual(try view("clear", 0, day: false).symbol, "moon")
        XCTAssertEqual(try view("rain", 3).precip, "Rain next 3 h · 0.1 in")
        XCTAssertEqual(try view("rain", 1).precip, "Rain next 3 h · < 0.1 in")
        XCTAssertEqual(try view("snow", 2).precip, "Snow next 3 h", "snow has no amount")
        XCTAssertEqual(try view("snow", 0).precip, "No snow next 3 h")
        XCTAssertNil(try view("rain", nil).precip)
    }

    func testDrivingPrefersTheServersDrive() throws {
        let now = try snapshot("driving", extra: #","drive":{"started_at":"2026-11-17T12:00:00Z","distance_mi":12.4,"duration_s":1500,"energy_kwh":4.1,"efficiency_mi_per_kwh":3.02}"#)
        let summary = NowSummary(input: input(gear: "D", charger: "charger_disconnected", kw: 0, kph: 80), now: now, clock: at)
        DisplayUnits.demo = true
        defer { DisplayUnits.demo = false }
        let live = LiveDrive(elapsedSec: 60, distanceMi: 0.5, energyUsedKwh: 0.2, miPerKwh: 2.5)
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: live, fix: nil, placeLabel: nil, clock: at))
        guard case .figures(let big, let unit, let sub, _, let stats) = model.content else { return XCTFail("figures") }
        XCTAssertEqual(big, "50")
        XCTAssertEqual(unit, "mph")
        XCTAssertEqual(sub, "This drive so far")
        XCTAssertEqual(stats.map(\.label), ["Distance", "Time", "Efficiency", "Energy used"])
        XCTAssertEqual(stats.first?.value, "12.4 mi")
        // Without the server's drive the live drive fills in, its efficiency only from a mile on.
        let fallback = NowSlotModel.drive(snapshot: try snapshot("driving"), live: live)
        XCTAssertEqual(fallback?.distanceMi, 0.5)
        XCTAssertNil(fallback?.miPerKwh)
        XCTAssertNil(NowSlotModel.drive(snapshot: nil, live: nil))
    }

    func testChargingNeedsPower() throws {
        let now = try snapshot("charging")
        let none = NowSummary(input: input(gear: "P", charger: "charger_connected", kw: 0), now: now, clock: at)
        XCTAssertNil(NowSlotModel.make(summary: none, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, placeLabel: nil, clock: at))
    }

    func testChargingWithoutASessionEstimatesRangeAndRate() throws {
        let start = at.addingTimeInterval(-3600)
        let iso = ISO8601DateFormatter().string(from: start)
        let now = try snapshot("charging", extra: #","charge":{"started_at":"\#(iso)","soc_start":40,"peak_kw":11}"#)
        let summary = NowSummary(input: input(gear: "P", charger: "charger_connected", kw: 11), now: now, clock: at)
        DisplayUnits.demo = true
        defer { DisplayUnits.demo = false }
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, placeLabel: nil, clock: at))
        guard case .figures(_, _, _, _, let stats) = model.content else { return XCTFail("figures") }
        let range = try XCTUnwrap(stats.first { $0.label == "Range added" })
        XCTAssertTrue(range.value.hasPrefix("≈ +"), range.value)
        XCTAssertTrue(try XCTUnwrap(stats.first { $0.label == "Rate" }).value.hasPrefix("≈ "))
    }

    func testRenderEachSlot() throws {
        let vehicle = try Fixture.decode([Vehicle].self, "vehicles")[0]
        let now = try snapshot("driving", extra: #","drive":{"started_at":"2026-11-17T12:00:00Z","distance_mi":12.4,"duration_s":1500,"energy_kwh":4.1,"efficiency_mi_per_kwh":3.02}"#)
        let summary = NowSummary(input: input(gear: "D", charger: "charger_disconnected", kw: 0, kph: 80), now: now, clock: at)
        let card = NowCard(vehicle: vehicle, summary: summary, clock: at, v2: true, slotOn: true, snapshot: now)
            .padding().frame(width: 393)
        let image = try XCTUnwrap(ImageRenderer(content: card).uiImage)
        XCTAssertGreaterThan(image.size.height, 200)
    }
}
