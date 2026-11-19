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
        XCTAssertNil(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, home: nil, clock: at), "no fix, the picture stays")
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: (34.1, -118.1), home: .init(lat: 34.1, lon: -118.1, label: nil), clock: at))
        guard case .parked(_, _, let label, let weather?) = model.content else { return XCTFail("parked") }
        XCTAssertEqual(label, "Home")
        XCTAssertEqual(weather.precip, "No rain next 3 h")
        XCTAssertTrue(model.isParked)
    }

    func testASleepingTruckShowsItsLastFixOnTheMap() throws {
        let now = try snapshot("asleep")
        let summary = NowSummary(input: .init(socPct: 60, rangeKm: 250, chargeTargetPct: 80, gear: "P", powerState: "sleep", chargerState: "charger_disconnected", chargerPowerKw: 0, at: at, packKwh: 100, speedKph: nil), now: now, clock: at)
        XCTAssertEqual(summary.kind, .asleep)
        XCTAssertNil(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, home: nil, clock: at), "no fix, the picture stays")
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: (34.1, -118.1), home: nil, clock: at))
        XCTAssertTrue(model.isParked)
    }

    func testASleepingTruckWithOnlyALastFixShowsTheMapAndTheSleepFigures() throws {
        let since = ISO8601DateFormatter().string(from: at.addingTimeInterval(-(2 * 3600 + 15 * 60)))
        let extra = #","last_fix":{"lat":34.1,"lon":-118.1,"at":"\#(since)"},"asleep_since":"\#(since)","asleep_today_s":67200"#
        let now = try snapshot("asleep", extra: extra)
        XCTAssertEqual(now.lastFix?.lat, 34.1)
        XCTAssertEqual(now.asleepTodayS, 67200)
        let fix = try XCTUnwrap(NowSlotModel.fix(snapshot: now, truck: nil), "the live state has none, the last fix stands in")
        let summary = NowSummary(input: .init(socPct: 60, rangeKm: 250, chargeTargetPct: 80, gear: "P", powerState: "sleep", chargerState: "charger_disconnected", chargerPowerKw: 0, at: at, packKwh: 100, speedKph: nil), now: now, clock: at)
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: fix, home: nil, clock: at))
        XCTAssertTrue(model.isParked)
        XCTAssertEqual(model.sleep, [.init(label: "Asleep for", value: "2 h 15 min", note: nil), .init(label: "Asleep today", value: "18 h 40 min", note: nil)])
        XCTAssertTrue(model.spoken.contains("Asleep today"))
        // The live state's fix wins; none anywhere is nil; a parked truck shows no sleep figures.
        XCTAssertEqual(NowSlotModel.fix(snapshot: now, truck: (1, 2))?.lat, 1)
        XCTAssertNil(NowSlotModel.fix(snapshot: try snapshot("asleep"), truck: nil))
        let parked = try snapshot("parked", extra: extra)
        let ps = NowSummary(input: input(gear: "P", charger: "charger_disconnected", kw: 0), now: parked, clock: at)
        XCTAssertEqual(try XCTUnwrap(NowSlotModel.make(summary: ps, snapshot: parked, session: nil, charge: nil, liveDrive: nil, fix: fix, home: nil, clock: at)).sleep, [])
    }

    func testASleepingTruckKeepsItsSlotPastTheStaleAndOfflineLimits() throws {
        func summary(_ state: String, power: String, charger: String = "charger_disconnected", after: TimeInterval) throws -> (NowSummary, NowSnapshot) {
            let now = try snapshot(state)
            let input = NowSummary.Input(socPct: 60, rangeKm: 250, chargeTargetPct: 80, gear: "P", powerState: power, chargerState: charger, chargerPowerKw: 0, at: at, packKwh: 100, speedKph: nil)
            return (NowSummary(input: input, now: now, clock: at.addingTimeInterval(after)), now)
        }
        // About 31 min: stale. About 3 h: offline. Both keep the slot, and the map is built from the fix.
        let cases: [(TimeInterval, NowSummary.Presence)] = [(31 * 60, .stale), (3 * 3600, .offline)]
        for (after, presence) in cases {
            let (s, now) = try summary("asleep", power: "sleep", after: after)
            XCTAssertEqual(s.presence, presence)
            XCTAssertTrue(NowSlotModel.shown(summary: s))
            XCTAssertNotNil(NowSlotModel.make(summary: s, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: (34.1, -118.1), home: nil, clock: at.addingTimeInterval(after)))
        }
        // Not asleep: a quiet or offline truck keeps the picture, as before.
        let quiet: [TimeInterval] = [20 * 60, 3 * 3600]
        for after in quiet {
            let (s, _) = try summary("parked", power: "ready", after: after)
            XCTAssertFalse(NowSlotModel.shown(summary: s))
        }
        let (stale, _) = try summary("charging", power: "ready", charger: "chrgr_sts_connected_charging", after: 20 * 60)
        XCTAssertFalse(NowSlotModel.shown(summary: stale))
        let (fresh, _) = try summary("parked", power: "ready", after: 10)
        XCTAssertTrue(NowSlotModel.shown(summary: fresh))
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
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: live, fix: nil, home: nil, clock: at))
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
        XCTAssertNil(NowSlotModel.make(summary: none, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, home: nil, clock: at))
    }

    func testChargingWithoutASessionEstimatesRangeAndRate() throws {
        let start = at.addingTimeInterval(-3600)
        let iso = ISO8601DateFormatter().string(from: start)
        let now = try snapshot("charging", extra: #","charge":{"started_at":"\#(iso)","soc_start":40,"peak_kw":11}"#)
        let summary = NowSummary(input: input(gear: "P", charger: "charger_connected", kw: 11), now: now, clock: at)
        DisplayUnits.demo = true
        defer { DisplayUnits.demo = false }
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, home: nil, clock: at))
        guard case .figures(_, _, _, _, let stats) = model.content else { return XCTFail("figures") }
        let range = try XCTUnwrap(stats.first { $0.label == "Range added" })
        XCTAssertTrue(range.value.hasPrefix("≈ +"), range.value)
        XCTAssertTrue(try XCTUnwrap(stats.first { $0.label == "Rate" }).value.hasPrefix("≈ "))
    }

    func testRateStaysApproximateWhenTheSessionSendsZero() throws {
        let start = at.addingTimeInterval(-3600)
        let iso = ISO8601DateFormatter().string(from: start)
        let now = try snapshot("charging", extra: #","charge":{"started_at":"\#(iso)","soc_start":40,"peak_kw":11}"#)
        let summary = NowSummary(input: input(gear: "P", charger: "charger_connected", kw: 11), now: now, clock: at)
        let session = LiveSession(active: true, timeElapsedSeconds: 3600, powerKw: 11, kilometersChargedPerHour: 0, rangeAddedKm: 0, totalChargedEnergyKwh: 8)
        DisplayUnits.demo = true
        defer { DisplayUnits.demo = false }
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: session, charge: nil, liveDrive: nil, fix: nil, home: nil, clock: at))
        guard case .figures(_, _, _, _, let stats) = model.content else { return XCTFail("figures") }
        XCTAssertTrue(try XCTUnwrap(stats.first { $0.label == "Rate" }).value.hasPrefix("≈ "))
        XCTAssertTrue(try XCTUnwrap(stats.first { $0.label == "Range added" }).value.hasPrefix("≈ +"))
    }

    func testChargingCostIsTheServersWithItsSourceAndTheChargerChip() throws {
        let start = at.addingTimeInterval(-3600)
        let iso = ISO8601DateFormatter().string(from: start)
        let now = try snapshot("charging", extra: #","charge":{"started_at":"\#(iso)","soc_start":40,"peak_kw":11,"cost":4.2,"currency":"USD","cost_source":"home_rate","cost_is_estimate":true}"#)
        let summary = NowSummary(input: input(gear: "P", charger: "charger_connected", kw: 11), now: now, clock: at)
        let home = NowSlotModel.Home(lat: 34.1, lon: -118.1, label: "Casa")
        let atHome = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: (34.1, -118.1), home: home, clock: at))
        guard case .figures(_, _, _, _, let stats) = atHome.content else { return XCTFail("figures") }
        let cost = try XCTUnwrap(stats.first { $0.label == "Cost so far" })
        XCTAssertTrue(cost.value.hasPrefix("≈ "), cost.value)
        XCTAssertEqual(cost.note, "at your home rate")
        XCTAssertEqual(atHome.chips.map(\.text), ["Home charger"])
        let away = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: (35.0, -118.1), home: home, clock: at))
        XCTAssertEqual(away.chips.map(\.text), ["Public charger"])
        let noHome = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: (35.0, -118.1), home: nil, clock: at))
        XCTAssertEqual(noHome.chips.map(\.text), [])
        let noFix = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, home: home, clock: at))
        XCTAssertEqual(noFix.chips.map(\.text), [])
        XCTAssertTrue(atHome.spoken.contains("Cost so far"), "what is shown is what is spoken")
        XCTAssertTrue(atHome.spoken.contains("Home charger"))
    }

    func testDrivingChipsAndTheLiveFallbackNeedsAPercentOfBattery() throws {
        let now = try snapshot("driving")
        let summary = NowSummary(input: input(gear: "D", charger: "charger_disconnected", kw: 0, kph: 80), now: now, clock: at)
        let model = try XCTUnwrap(NowSlotModel.make(summary: summary, snapshot: now, session: nil, charge: nil, liveDrive: nil, fix: nil, home: nil, gear: "d", mode: "off_road_auto", clock: at))
        XCTAssertEqual(model.chips.map(\.text), ["D", "All-Terrain"])
        // 3 mi on 0.4 kWh of a 100 kWh pack is under a percent: no efficiency from the live drive.
        let thin = LiveDrive(elapsedSec: 300, distanceMi: 3, energyUsedKwh: 0.4, miPerKwh: 7.5)
        XCTAssertNil(NowSlotModel.drive(snapshot: nil, live: thin, packKwh: 100)?.miPerKwh)
        let enough = LiveDrive(elapsedSec: 300, distanceMi: 3, energyUsedKwh: 1.2, miPerKwh: 2.5)
        XCTAssertEqual(NowSlotModel.drive(snapshot: nil, live: enough, packKwh: 100)?.miPerKwh, 2.5)
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
