import MapLibre
import XCTest
@testable import Rivolt

/// The iPhone says a leg's weather in the web's words and rules (web/src/plan/legWeather.test.ts).
final class LegWeatherTests: XCTestCase {
    private func leg(_ multiplier: Double = 1, temp: Double? = nil, air: Double? = nil, wind: Double? = nil, precip: Double? = nil, snow: Bool? = nil,
                     climb: Double? = nil, descent: Double? = nil) -> TripWeatherLeg {
        TripWeatherLeg(multiplier: multiplier, tempC: temp, airTempC: air, headwindKph: wind, precipMm: precip, snow: snow, climbM: climb, descentM: descent)
    }

    /// The map draws its labels from the glyphs the app ships (asset://Map/glyphs, 256 characters a file): a label with one character from a range that
    /// is not there is not drawn at all, and none of the weather labels was (the wind arrows are U+2197/U+2199, the minus sign U+2212).
    func testEveryCharacterOfAMapLabelHasItsGlyphsInTheApp() throws {
        let legs = [leg(temp: -4, wind: 30), leg(temp: 12, wind: -30, precip: 2), leg(temp: -20, snow: true), leg(temp: 30)]
        var labels: [String] = [PlanRouteLogic.nightLabel(nil, place: nil)]
        for l in legs {
            for (celsius, metric) in [(false, false), (true, true)] {
                labels += [LegWeather.mapLabel(LegWeather.facts(l, celsius: celsius, metric: metric))].compactMap { $0 }
            }
        }
        XCTAssertTrue(labels.contains { $0.contains("↙") } && labels.contains { $0.contains("↗") } && labels.contains { $0.contains("−") }, "\(labels)")
        for label in labels {
            for c in label.unicodeScalars {
                let start = Int(c.value) / 256 * 256
                let file = "\(start)-\(start + 255)"
                XCTAssertNotNil(Bundle.main.url(forResource: file, withExtension: "pbf", subdirectory: "Map/glyphs/Noto Sans Medium"),
                                "\(label): \(c) (U+\(String(c.value, radix: 16, uppercase: true))) needs Map/glyphs/Noto Sans Medium/\(file).pbf")
            }
        }
    }

    /// Names come from the data (a night's hotel, a charger): the usual ones draw whole, and one with characters the app has no glyphs for keeps the rest
    /// rather than drawing nothing.
    func testANameOnTheMapDrawsWithTheGlyphsTheAppHas() {
        XCTAssertFalse(MapGlyphs.ranges.isEmpty, "the glyphs are in the app")
        for name in ["Example Hotel & Suites", "Hôtel du Café", "Joe’s Inn – Downtown", "Example Lodge™", "Ñandú Motel", "Łódź Inn", "Charger #3 (150 kW)"] {
            let label = PlanRouteLogic.nightLabel(nil, place: nil) + ": " + name
            XCTAssertEqual(MapGlyphs.drawable(label), label, "\(name) draws as it is")
        }
        XCTAssertEqual(MapGlyphs.drawable("Night: Example 東京 Hotel 🏨"), "Night: Example Hotel")
        for c in MapGlyphs.drawable("Γεια Привет שלום مرحبا 你好 🙂 ok").unicodeScalars {
            XCTAssertTrue(MapGlyphs.canDraw(c), "U+\(String(c.value, radix: 16, uppercase: true)) is left in but has no glyphs")
        }
    }

    func testTemperatureAndAHeadwindInTheAccountsUnits() {
        let f = LegWeather.facts(leg(1.1, temp: 5, wind: 40, precip: 2), celsius: false, metric: false)
        XCTAssertEqual(LegWeather.tags(f), ["41 °F", "25 mph headwind", "rain"])
        XCTAssertEqual(f?.tempShort, "41°")
        XCTAssertEqual(LegWeather.tags(LegWeather.facts(leg(temp: 5, wind: 40), celsius: true, metric: true)), ["5 °C", "40 km/h headwind"])
    }

    func testANegativeHeadwindIsATailwindAndALightBreezeIsLeftOut() {
        XCTAssertEqual(LegWeather.tags(LegWeather.facts(leg(wind: -20), celsius: false, metric: false)), ["12 mph tailwind"])
        XCTAssertEqual(LegWeather.tags(LegWeather.facts(leg(wind: 5), celsius: false, metric: false)), [])
    }

    func testRainOnlyWhenTheWetFactorAppliedAndSnowOnlyWhenTheServerSaysSnow() {
        XCTAssertNil(LegWeather.facts(leg(precip: 0.5), celsius: false, metric: false))
        XCTAssertEqual(LegWeather.facts(leg(1.05, precip: 0.6), celsius: false, metric: false)?.rain, true)
        let snow = LegWeather.facts(leg(1.2, temp: -6, precip: 3, snow: true), celsius: false, metric: false)
        XCTAssertEqual(LegWeather.tags(snow), ["21 °F", "snow expected"])
        XCTAssertEqual(snow?.rain, false, "snow is not also rain")
    }

    func testARealMinusBelowZeroAndNothingForAnEmptyLeg() {
        XCTAssertEqual(LegWeather.tags(LegWeather.facts(leg(temp: -10), celsius: true, metric: true)), ["−10 °C"])
        XCTAssertNil(LegWeather.facts(leg(), celsius: false, metric: false))
        XCTAssertNil(LegWeather.facts(nil, celsius: false, metric: false))
    }

    func testTheBadgeShowsTheAirTemperatureAndFeelsLikeIsForTheDetails() {
        let f = LegWeather.facts(leg(temp: -6, air: 0, wind: 20), celsius: false, metric: false)
        XCTAssertEqual(f?.temp, "32 °F")
        XCTAssertEqual(f?.feels, "21 °F")
        XCTAssertNil(LegWeather.facts(leg(temp: 5, air: 5), celsius: false, metric: false)?.feels, "the same: nothing to add")
    }

    func testTheMapLabelIsShortAndSaysSnowWithoutANumber() {
        XCTAssertEqual(LegWeather.mapLabel(LegWeather.facts(leg(temp: 5, wind: 40, precip: 2), celsius: false, metric: false)), "41° ↙ 25 mph rain")
        XCTAssertEqual(LegWeather.mapLabel(LegWeather.facts(leg(temp: -6, wind: -20, snow: true), celsius: false, metric: false)), "21° snow expected")
        XCTAssertEqual(LegWeather.mapLabel(LegWeather.facts(leg(wind: -20), celsius: false, metric: false)), "↗ 12 mph")
        XCTAssertNil(LegWeather.mapLabel(nil))
    }

    func testTheOneEstimateLineWithOrWithoutASetupFactor() {
        XCTAssertEqual(LegWeather.estimateLine(multiplier: 1.14, withSetup: true), "Weather and your setup: +14% (estimate)")
        XCTAssertEqual(LegWeather.estimateLine(multiplier: 1.14, withSetup: false), "Weather: +14% (estimate)")
        XCTAssertEqual(LegWeather.estimateLine(multiplier: 0.92, withSetup: false), "Weather: −8% (estimate)")
        XCTAssertNil(LegWeather.estimateLine(multiplier: 1.003, withSetup: false))
    }

    func testTheClimbLineInTheAccountsUnitAndWaitsForBothFigures() {
        XCTAssertEqual(LegWeather.climbLine(climbM: 579, descentM: 670, metric: false), "Climb +1,900 ft · descent −2,198 ft")
        XCTAssertEqual(LegWeather.climbLine(climbM: 579, descentM: 670, metric: true), "Climb +579 m · descent −670 m")
        XCTAssertNil(LegWeather.climbLine(climbM: nil, descentM: 670, metric: false))
        XCTAssertTrue(LegWeather.terrainCredit.hasPrefix("Terrain: USGS 3DEP"))
    }

    func testAPlanWithWeatherDecodesAndCarriesTheProfileFlag() throws {
        var plan = try Fixture.decode(TripPlan.self, "trip_plan_rivian_shape")
        XCTAssertNil(PlanRouteLogic.Weather(plan), "a plan with no weather says none")
        let json = #"{"Status":"SUCCESS","Routes":[],"weather_adjustment":{"profile_multiplier":1.03,"legs":[{"multiplier":1.14,"temp_c":3,"air_temp_c":5,"headwind_kph":40,"precip_mm":2,"climb_m":579,"descent_m":100},{"multiplier":1.2,"snow":true}]}}"#
        plan = try JSON.decoder().decode(TripPlan.self, from: Data(json.utf8))
        let w = try XCTUnwrap(PlanRouteLogic.Weather(plan, celsius: false, metric: false))
        XCTAssertEqual(w.legs.count, 2)
        XCTAssertTrue(w.withSetup)
        XCTAssertEqual(w.legs[0].airTempC, 5)
        XCTAssertEqual(w.legs[1].snow, true)
        let plain = try JSON.decoder().decode(TripPlan.self, from: Data(#"{"Status":"SUCCESS","Routes":[],"weather_adjustment":{"legs":[{"multiplier":1.1,"temp_c":5}]}}"#.utf8))
        XCTAssertFalse(try XCTUnwrap(PlanRouteLogic.Weather(plain)).withSetup)
    }

    func testOneLabelPerLegAtItsMiddleAndNoneWithoutWeather() throws {
        let plan = try Fixture.decode(TripPlan.self, "trip_plan_rivian_shape")
        let route = try XCTUnwrap(plan.best)
        let count = route.waypoints.count - 1
        let legs = (0..<count).map { TripWeatherLeg(multiplier: 1.1, tempC: Double($0) * 3, airTempC: nil, headwindKph: nil, precipMm: nil, snow: nil, climbM: nil, descentM: nil) }
        let json = try JSONEncoder().encode(["legs": legs.map { ["multiplier": $0.multiplier, "temp_c": $0.tempC ?? 0] }])
        let adj = try JSON.decoder().decode(TripWeatherAdjustment.self, from: json)
        var p = plan; p.weatherAdjustment = adj
        let weather = try XCTUnwrap(PlanRouteLogic.Weather(p, celsius: false, metric: false))
        let tags = PlanRouteLogic.legTags(route: route, weather: weather, line: [])
        XCTAssertEqual(tags.count, count)
        XCTAssertEqual(tags.map(\.text), (0..<count).map { "\(Int((Double($0) * 3 * 1.8 + 32).rounded()))°" })
        let a = route.waypoints[0], b = route.waypoints[1]
        XCTAssertEqual(tags[0].coord.lat, ((a.latitude ?? 0) + (b.latitude ?? 0)) / 2, accuracy: 1e-9, "no drawn line: the straight middle")
        XCTAssertTrue(PlanRouteLogic.legTags(route: route, weather: nil, line: []).isEmpty, "the Weather button off: no labels")
    }

    func testTheMiddleOfALegFollowsTheDrawnLine() {
        let line = (0...10).map { RouteSnap.Coord(lat: 40 + Double($0) * 0.1, lon: -100 + (($0 % 2 == 0) ? 0 : 0.02)) }
        let mid = PlanRouteLogic.legMiddle(line: line, from: line[0], to: line[10])
        XCTAssertEqual(mid, line[5], "on the line, half way by vertex, not off it at the straight middle")
    }

    /// A real MapLibre view: the weather labels have a layer of their own, above the route and under the focus ring (a label the map cannot place is dropped by the map).
    @MainActor
    func testTheMapHasALayerForTheWeatherLabels() async throws {
        let line = (0..<10).map { RouteSnap.Coord(lat: 40 + Double($0) * 0.01, lon: -100 + Double($0) * 0.01) }
        var d = RouteSnap.Drawing(pieces: [RouteSnap.Piece(coords: line, speed: nil)], routedGaps: [], gaps: [], start: line.first, end: line.last, hasSpeed: false)
        d.legTags = [.init(coord: line[4], text: "41° ↙ 25 mph")]
        let coord = RouteMapView.Coordinator(drawing: d, onHealth: { _ in })
        let map = MLNMapView.displayOnly(styleJSON: ##"{"version":8,"sources":{},"layers":[{"id":"bg","type":"background","paint":{"background-color":"#101010"}}]}"##, watch: coord, interactive: false)
        for _ in 0..<100 where map.style?.layer(withIdentifier: "rivolt-route-legtags") == nil { try await Task.sleep(for: .milliseconds(100)) }
        let style = try XCTUnwrap(map.style)
        let ids = style.layers.map(\.identifier)
        let tags = try XCTUnwrap(ids.firstIndex(of: "rivolt-route-legtags"), "the weather labels have a layer")
        let focus = try XCTUnwrap(ids.firstIndex(of: "rivolt-route-stop-focus"))
        XCTAssertLessThan(tags, focus)
        XCTAssertNotNil(style.source(withIdentifier: "rivolt-route-legtags"))
    }
}
