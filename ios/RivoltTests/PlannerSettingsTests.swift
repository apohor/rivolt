import SwiftUI
import XCTest
@testable import Rivolt

/// The Trip planner page: what it sends, what it shows for a server that has the map settings and one that does not, and that it holds at large text.
@MainActor
final class PlannerSettingsTests: XCTestCase {
    private func saved(map: PlannerMapSettings? = PlannerMapSettings()) -> PlannerDefaults {
        PlannerDefaults(driveMode: "EVERYDAY", hasAdapter: true, gasPricePerGallon: 4, comparisonMpg: 20, askAdapter: true, map: map)
    }

    func testTheServersFullAnswerDecodesToTheDefaults() throws {
        // The shape backend-2's contract gives (a fixture of it comes with the server's change).
        let json = #"{"drive_mode":"EVERYDAY","has_adapter":true,"corridor":"normal","start_exclude_mi":0,"min_fast_kw":0,"layers_on":["dcfc"],"preferred_only":false,"weather_on":true,"radar_on":true}"#
        let p = try JSON.decoder().decode(PlannerPrefs.self, from: Data(json.utf8))
        let m = try XCTUnwrap(PlannerMapSettings(p))
        XCTAssertEqual(m.corridor, "normal")
        XCTAssertEqual(m.startExcludeMi, 0, "hide near the start is Off to begin with")
        XCTAssertEqual(m.minFastKw, 0)
        XCTAssertEqual(m.layersOn, ["dcfc"], "Fast is on to begin with")
        XCTAssertFalse(m.preferredOnly)
        XCTAssertTrue(m.weatherOn)
        XCTAssertTrue(m.radarOn, "Radar is on to begin with")
    }

    func testAnOlderServerHasNoMapSettingsAndTheGroupsAreNotShown() throws {
        // An older server sends just the two it knows (the checked-in fixture is the newer server's answer, with every field).
        let p = try JSON.decoder().decode(PlannerPrefs.self, from: Data(#"{"drive_mode":"EVERYDAY","has_adapter":true}"#.utf8))
        XCTAssertNil(PlannerMapSettings(p))
        XCTAssertNotNil(PlannerMapSettings(try Fixture.decode(PlannerPrefs.self, "settings_planner")), "the server's own fixture has the map settings")
        let d = PlannerDefaultsForm.draft(saved(map: nil))
        XCTAssertNil(d.map)
        var changed = d
        changed.driveMode = "DISTANCE"
        let sent = try XCTUnwrap(PlannerDefaultsForm.prefsPatch(changed, saved: saved(map: nil)))
        XCTAssertNil(sent.corridor, "an older server is sent only the two it knows")
        XCTAssertNil(sent.layersOn)
    }

    func testASaveSendsTheWholeSetSoNothingIsClearedAndNothingIsSentUnchanged() throws {
        let s = saved()
        var d = PlannerDefaultsForm.draft(s)
        XCTAssertNil(PlannerDefaultsForm.prefsPatch(d, saved: s), "opening and saving without a change sends nothing")
        d.map?.startExcludeMi = 25
        d.map?.layersOn = ["hotels", "dcfc"]
        let p = try XCTUnwrap(PlannerDefaultsForm.prefsPatch(d, saved: s))
        XCTAssertEqual(p.driveMode, "EVERYDAY", "the older two go too: a PUT that left them out would clear them")
        XCTAssertEqual(p.hasAdapter, true)
        XCTAssertEqual(p.corridor, "normal")
        XCTAssertEqual(p.startExcludeMi, 25)
        XCTAssertEqual(p.minFastKw, 0)
        XCTAssertEqual(p.layersOn, ["dcfc", "hotels"], "in the server's layer order, whatever order they were switched on")
        XCTAssertEqual(p.preferredOnly, false)
        XCTAssertEqual(p.weatherOn, true)
        XCTAssertEqual(p.radarOn, true, "kept as it is while the radar row is not shown")
        let enc = JSONEncoder(); enc.keyEncodingStrategy = .convertToSnakeCase; enc.outputFormatting = [.sortedKeys]
        XCTAssertEqual(String(data: try enc.encode(p), encoding: .utf8),
                       #"{"corridor":"normal","drive_mode":"EVERYDAY","has_adapter":true,"layers_on":["dcfc","hotels"],"min_fast_kw":0,"preferred_only":false,"radar_on":true,"start_exclude_mi":25,"weather_on":true}"#)
    }

    func testTheRadarDefaultRowIsThereOnlyWhereTheServerHasRadar() {
        XCTAssertTrue(PlannerSettingsCopy.showsRadarRow(.available))
        XCTAssertFalse(PlannerSettingsCopy.showsRadarRow(.unavailable), "no radar on this server: no row")
        XCTAssertFalse(PlannerSettingsCopy.showsRadarRow(.unknown), "not known yet: no row to flash and go")
    }

    func testAllLayersOffIsASetting() throws {
        let s = saved()
        var d = PlannerDefaultsForm.draft(s)
        d.map?.layersOn = []
        XCTAssertEqual(try XCTUnwrap(PlannerDefaultsForm.prefsPatch(d, saved: s)).layersOn, [], "an empty list, not an absent one")
    }

    func testTheWordsSayWhatEachSettingDoesAndNeverReadAsOnWhenItIsOff() {
        XCTAssertEqual(PlannerSettingsCopy.startChoices(metric: false).map(\.title), ["Off", "First 25 mi", "First 50 mi"])
        XCTAssertEqual(PlannerSettingsCopy.startChoices(metric: true).map(\.title), ["Off", "First 40 km", "First 80 km"])
        XCTAssertEqual(PlannerSettingsCopy.startChoices(metric: true).map(\.value), [0, 25, 50], "the server's values stay in miles")
        XCTAssertEqual(PlannerSettingsCopy.startNote, "Turn on to hide chargers in the first miles of the first day, if you usually start charged. Rivian's planned stops always show.")
        XCTAssertTrue(PlannerSettingsCopy.corridorNote(metric: false).hasSuffix("about 2 mi, 5 mi or 10 mi."))
        XCTAssertTrue(PlannerSettingsCopy.corridorNote(metric: true).hasSuffix("about 3 km, 8 km or 16 km."))
        XCTAssertEqual(PlannerSettingsCopy.weatherLabel, "Weather on each leg", "the web's label")
        XCTAssertEqual(PlannerSettingsCopy.weatherNote, "The temperature, wind, rain and snow on each leg, on by default.", "the web's note: what the badges really show")
        XCTAssertEqual(PlannerSettingsCopy.minFast.map(\.value), [0, 50, 150, 250])
        XCTAssertEqual(PlannerSettingsCopy.corridors.map(\.value), ["close", "normal", "wide"])
        XCTAssertEqual(PlannerSettingsCopy.layers.map(\.kind), ["dcfc", "l2", "hotels", "camp"], "the server's layer kinds, the charger words as titles")
        XCTAssertEqual(PlannerSettingsCopy.layers.map(\.title), ["Fast", "Medium", "Hotel", "Camping"])
        for note in [PlannerSettingsCopy.corridorNote(metric: false), PlannerSettingsCopy.startNote, PlannerSettingsCopy.minFastNote, PlannerSettingsCopy.layersNote,
                     PlannerSettingsCopy.preferredNote, PlannerSettingsCopy.weatherNote, PlannerSettingsCopy.radarNote] {
            XCTAssertTrue(note.hasSuffix("."), note)
            XCTAssertLessThan(note.split(separator: ".").count, 4, "a line or two, in plain words: \(note)")
        }
    }

    private func width(of view: some View, at width: CGFloat) async throws -> CGSize {
        let host = UIHostingController(rootView: view.frame(width: width, alignment: .topLeading))
        host.safeAreaRegions = []
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first, "a window scene")
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: width, height: 900)
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        host.view.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(500))
        return host.sizeThatFits(in: CGSize(width: width, height: 6000))
    }

    func testEveryChoiceRowFitsThePhoneAtTheLargestTextSize() async throws {
        for type in [DynamicTypeSize.large, .accessibility5] {
            for w in [CGFloat(393), 375] {
                let row = PlannerChoiceRow(label: PlannerSettingsCopy.minFastLabel, note: PlannerSettingsCopy.minFastNote, choices: PlannerSettingsCopy.minFast, selection: .constant(0))
                    .environment(\.dynamicTypeSize, type).padding(.horizontal, 16)
                let s = try await width(of: row, at: w)
                XCTAssertLessThanOrEqual(s.width, w, "\(type), \(w) pt: nothing runs off the edge (four segments would not fit at AX5, so it is a menu)")
            }
        }
    }
}
