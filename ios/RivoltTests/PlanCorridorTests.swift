import XCTest
@testable import Rivolt

@MainActor
final class PlanCorridorTests: XCTestCase {
    private final class Fake: CorridorLayersSource, @unchecked Sendable {
        var answer = CorridorLayers()
        var error: Error?
        private(set) var requests: [CorridorLayersRequest] = []
        func corridorLayers(_ body: CorridorLayersRequest) async throws -> CorridorLayers {
            requests.append(body)
            if let error { throw error }
            return answer
        }
    }

    private func poi(_ name: String, kinds: [String], slug: String? = nil, offKm: Double? = 1) -> CorridorPOI {
        CorridorPOI(lat: 40, lon: -100, name: name, isDCFC: kinds.contains("dcfc"), maxPowerKW: 150, evNetwork: nil, networkSlug: slug, networkName: slug?.uppercased(), alongKm: 10, offKm: offKm, kinds: kinds)
    }

    func testTheDecodedAnswerKeepsWhatTheMapAndTheCalloutNeed() throws {
        let json = #"{"pois":[{"lat":1,"lon":2,"name":"A","isDCFC":true,"maxPowerKW":250,"networkSlug":"ea","networkName":"Electrify America","alongKm":12.5,"offKm":2.1,"kinds":["dcfc"]}],"count":1,"total":1240,"next_after_km":300.5,"asof":"2026-10-20","route_km":600}"#
        let a = try JSON.decoder().decode(CorridorLayers.self, from: Data(json.utf8))
        XCTAssertEqual(a.total, 1240)
        XCTAssertEqual(a.asof, "2026-10-20")
        XCTAssertEqual(a.nextAfterKm, 300.5)
        XCTAssertEqual(a.pois.first?.networkSlug, "ea")
        XCTAssertEqual(a.pois.first?.offKm, 2.1)
    }

    func testTurningALayerOnAsksOnceForThoseKindsAndTurningThemAllOffClears() async throws {
        let src = Fake()
        src.answer = CorridorLayers(pois: [poi("A", kinds: ["dcfc"]), poi("B", kinds: ["hotels"]), poi("C", kinds: ["camp"])], total: 3, asof: "2026-10-20")
        let m = TripPlanModel()
        // (no plan: nothing to ask about yet)
        await m.setLayer(.chargers, on: true, source: src)
        XCTAssertTrue(src.requests.isEmpty)
        XCTAssertEqual(m.layers, [.chargers])
    }

    func testPreferredNetworksAreProminentAndPreferredOnlyHidesTheRestOfTheChargers() {
        let m = TripPlanModel()
        m.setPreferred(["ea"])
        XCTAssertEqual(MapLayer.hotels.kind, "hotels")
        XCTAssertEqual(MapLayer.camps.kind, "camp")
        m.setPreferredOnly(true)
        XCTAssertTrue(m.preferredOnly)
        m.setPreferred([])
        m.setPreferredOnly(true)
        XCTAssertFalse(m.preferredOnly, "no preferred networks: nothing to filter by")
    }

    func testTheCalloutNamesKindPowerNetworkAndDetourButNeverFreeOrWorking() {
        let c = AlongCharger(lat: 1, lon: 2, name: "Sample Inn", isDCFC: false, maxPowerKW: 7, evNetwork: nil, kind: "hotel", preferred: false, offKm: 1.2, networkName: nil)
        let t = PlanSheetLogic.chargerDetail(c, offRoute: "about 1 mi off the route")
        XCTAssertTrue(t.contains("Hotel with a listed Level 2 charger"))
        XCTAssertNil(t.range(of: "free|working|available", options: [.regularExpression, .caseInsensitive]))
        let pref = AlongCharger(lat: 1, lon: 2, name: "X", isDCFC: true, maxPowerKW: 250, evNetwork: "Raw", kind: "dcfc", preferred: true, offKm: nil, networkName: "Electrify America")
        XCTAssertEqual(PlanSheetLogic.chargerDetail(pref, offRoute: nil), "250 kW · Electrify America · a preferred network")
    }
}
