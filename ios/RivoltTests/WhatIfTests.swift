import XCTest
@testable import Rivolt

@MainActor
final class WhatIfTests: XCTestCase {
    private func decode(_ json: String) throws -> WhatIfResponse { try JSON.decoder().decode(WhatIfResponse.self, from: Data(json.utf8)) }

    func testTheChangeLeadsThenAnEstimatedClockTimeThenTheChargeAsAChangeThenOneWayWarnings() throws {
        let r = try decode(#"{"estimate":true,"estimated":false,"notice":"estimate_replan","segment":{"from":1,"to":2},"added_m":12400,"added_sec":780,"at_edit":{"arrival_at":"2026-11-14T17:25:00Z"},"destination":{"arrival_at":"2026-11-14T20:45:00Z"},"next_charge":{"index":2,"is_destination":false,"soc_drop_pts":4.1},"warnings":["may_not_reach_next_charge"]}"#)
        let lines = WhatIfLogic.lines(r, metric: false, zone: TimeZone(identifier: "UTC")!)
        XCTAssertEqual(lines.first, "+13 min · +7.7 mi")
        XCTAssertTrue(lines[1].hasPrefix("About ") && lines[1].hasSuffix("at the destination (estimate)"), lines[1])
        XCTAssertEqual(lines[2], "About 4 points lower at the next charger")
        XCTAssertEqual(lines[3], "May not reach the next charger (estimate)")
        XCTAssertEqual(lines.last, WhatIfLogic.notice, "always said to be an estimate")
        // Never an absolute battery figure, never "reachable".
        XCTAssertNil(lines.joined().range(of: "%|reachable|will reach", options: .regularExpression))
    }

    func testNoClaimWhenTheAnswerHasNone() throws {
        let r = try decode(#"{"added_sec":-300,"added_m":-2000}"#)
        let lines = WhatIfLogic.lines(r, metric: true, zone: .current)
        XCTAssertEqual(lines.first, "−5 min · −2 km")
        XCTAssertEqual(lines.count, 2, "the change, and the estimate notice: no clock time, no charge, no warning")
        XCTAssertTrue(WhatIfLogic.lines(try decode("{}"), metric: false, zone: .current).isEmpty)
        let rough = WhatIfLogic.lines(try decode(#"{"estimated":true,"added_sec":600}"#), metric: false, zone: .current)
        XCTAssertTrue(rough.contains(WhatIfLogic.roughNote))
    }

    func testTheDurationReadsInMinutesThenHours() {
        XCTAssertEqual(WhatIfLogic.signedDuration(780), "+13 min")
        XCTAssertEqual(WhatIfLogic.signedDuration(3900), "+1 h 5 min")
        XCTAssertEqual(WhatIfLogic.signedDuration(7200), "+2 h")
        XCTAssertEqual(WhatIfLogic.signedDuration(-60), "−1 min")
        XCTAssertNil(WhatIfLogic.signedDuration(nil))
    }

    func testANewStopGoesBeforeTheFirstStopTheRoadReachesAfterIt() {
        let line = (0..<20).map { RouteSnap.Coord(lat: 40 + Double($0) * 0.1, lon: -100) }
        let stops = [WhatIfStop(lat: 40, lon: -100, source: "origin"), WhatIfStop(lat: 41, lon: -100, source: "planner"), WhatIfStop(lat: 41.9, lon: -100, source: "destination")]
        XCTAssertEqual(WhatIfLogic.insertionIndex(stops: stops, point: RouteSnap.Coord(lat: 40.4, lon: -99.9), line: line), 1)
        XCTAssertEqual(WhatIfLogic.insertionIndex(stops: stops, point: RouteSnap.Coord(lat: 41.5, lon: -99.9), line: line), 2)
        XCTAssertEqual(WhatIfLogic.insertionIndex(stops: stops, point: RouteSnap.Coord(lat: 41.95, lon: -99.9), line: line), 2, "never after the destination")
        XCTAssertEqual(WhatIfLogic.insertionIndex(stops: stops, point: RouteSnap.Coord(lat: 40.4, lon: -99.9), line: []), 2, "no line: before the destination")
    }

    func testTheRequestCarriesThePlansStopsAndOneAddEdit() throws {
        let route = try XCTUnwrap(try Fixture.decode(TripPlan.self, "trip_plan").best)
        let req = try XCTUnwrap(WhatIfLogic.addRequest(route, point: RouteSnap.Coord(lat: 47.3, lon: -120.0), line: [], target: 140))
        XCTAssertEqual(req.edit.kind, "add")
        XCTAssertEqual(req.stops.first?.source, "origin")
        XCTAssertEqual(req.stops.last?.source, "destination")
        XCTAssertEqual(req.targetArrivalSocPercent, 100, "the server refuses outside 0 to 100: clamped")
        XCTAssertEqual(req.edit.via, WhatIfPoint(lat: 47.3, lon: -120.0))
    }
}
