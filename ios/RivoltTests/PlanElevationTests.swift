import SwiftUI
import XCTest
@testable import Rivolt

/// The elevation chart's arithmetic and its decode (web/src/plan/elevation.test.ts), and the chart at the largest text size.
@MainActor
final class PlanElevationTests: XCTestCase {
    private let profile = ElevationProfile(points: [[0, 100], [1000, 300], [3000, 200]], waypointAtM: [0, 1000, 3000])

    func testThePlanDecodesItsProfileAndAPlanWithoutOneHasNone() throws {
        let with = #"{"Status":"SUCCESS","Routes":[],"elevation_profile":{"points":[[0,-86],[250,-41],[445,-6]],"waypoint_at_m":[0,null,445]}}"#
        let plan = try JSON.decoder().decode(TripPlan.self, from: Data(with.utf8))
        XCTAssertEqual(plan.elevationProfile?.points, [[0, -86], [250, -41], [445, -6]])
        XCTAssertEqual(plan.elevationProfile?.waypointAtM, [0, nil, 445])
        let without = try JSON.decoder().decode(TripPlan.self, from: Data(#"{"Status":"SUCCESS","Routes":[]}"#.utf8))
        XCTAssertNil(without.elevationProfile)
        let broken = try JSON.decoder().decode(TripPlan.self, from: Data(#"{"Status":"SUCCESS","Routes":[],"elevation_profile":"no"}"#.utf8))
        XCTAssertNil(broken.elevationProfile, "a profile that does not decode leaves the plan whole")
    }

    func testOnlyAProfileThatRunsForwardIsDrawn() {
        XCTAssertNotNil(PlanElevation.usable(profile))
        XCTAssertNil(PlanElevation.usable(nil))
        XCTAssertNil(PlanElevation.usable(.init(points: [[0, 1]], waypointAtM: [])), "one point")
        XCTAssertNil(PlanElevation.usable(.init(points: [[0, 1], [0, 2]], waypointAtM: [])), "no length")
        XCTAssertNil(PlanElevation.usable(.init(points: [[0, 1], [500, 2], [400, 3]], waypointAtM: [])), "back in distance")
        XCTAssertNil(PlanElevation.usable(.init(points: [[0, 1], [500]], waypointAtM: [])), "half a point")
        XCTAssertNil(PlanElevation.usable(.init(points: [[0, 1], [.nan, 2]], waypointAtM: [])), "not a number")
    }

    func testHeightIsInterpolatedAndStopsAreTheSharesBetweenTheEnds() {
        XCTAssertEqual(PlanElevation.height(profile, at: 0), 100, accuracy: 1e-9)
        XCTAssertEqual(PlanElevation.height(profile, at: 1.0 / 6), 200, accuracy: 1e-9, "half way up the first stretch")
        XCTAssertEqual(PlanElevation.height(profile, at: 1), 200, accuracy: 1e-9)
        XCTAssertEqual(PlanElevation.height(profile, at: 7), 200, accuracy: 1e-9, "past the end stays on it")
        let stops = PlanElevation.stops(.init(points: profile.points, waypointAtM: [0, 1500, nil, 3000]))
        XCTAssertEqual(stops.map(\.index), [1])
        XCTAssertEqual(stops.first?.share ?? 0, 0.5, accuracy: 1e-9)
    }

    func testTheReadoutIsInTheAccountsUnitsAndGivesNoEnergyFigure() {
        XCTAssertEqual(PlanElevation.readout(profile, share: 1.0 / 6, metric: false), "0 mi · 656 ft")
        XCTAssertEqual(PlanElevation.readout(profile, share: 1.0 / 6, metric: true), "1 km · 200 m")
        XCTAssertEqual(PlanElevation.readout(profile, share: nil, metric: true), "100 to 300 m")
        XCTAssertEqual(PlanElevation.readout(profile, share: nil, metric: false), "328 to 984 ft")
        XCTAssertEqual(PlanElevation.distance(1_609_344, metric: false), "1,000 mi")
        XCTAssertTrue(PlanElevation.label(profile, metric: true).hasPrefix("Elevation along the route, 100 to 300 m, over 3 km"))
        XCTAssertFalse(PlanElevation.context.lowercased().contains("kwh"))
    }

    private let line = [RouteSnap.Coord(lat: 47, lon: -122), RouteSnap.Coord(lat: 47, lon: -121), RouteSnap.Coord(lat: 47, lon: -120)]

    func testAShareFindsItsPlaceOnTheLineAndAPlaceItsShare() throws {
        let mid = try XCTUnwrap(PlanElevation.point(on: line, at: 0.5))
        XCTAssertEqual(mid.lon, -121, accuracy: 1e-9)
        let quarter = try XCTUnwrap(PlanElevation.point(on: line, at: 0.25))
        XCTAssertEqual(quarter.lon, -121.5, accuracy: 1e-9)
        XCTAssertNil(PlanElevation.point(on: [line[0]], at: 0.5))
        let hit = try XCTUnwrap(PlanElevation.share(of: RouteSnap.Coord(lat: 47.001, lon: -121.5), on: line))
        XCTAssertEqual(hit.share, 0.25, accuracy: 1e-3)
        XCTAssertEqual(hit.offMeters, 111, accuracy: 3, "a thousandth of a degree off the line")
        XCTAssertNil(PlanElevation.share(of: line[0], on: []))
    }

    /// At the largest text sizes the chart still fits the phone and keeps its height.
    func testTheChartFitsThePhoneAtTheLargestTextSize() async throws {
        let view = ElevationChartView(profile: profile, metric: false, share: .constant(0.5)).environment(\.dynamicTypeSize, .accessibility5)
        let host = UIHostingController(rootView: view.frame(width: 393, alignment: .topLeading))
        host.safeAreaRegions = []
        let scene = try XCTUnwrap(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first, "a window scene")
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 393, height: 900)
        window.rootViewController = host
        window.makeKeyAndVisible()
        defer { window.isHidden = true }
        host.view.layoutIfNeeded()
        try await Task.sleep(for: .milliseconds(500))
        let s = host.sizeThatFits(in: CGSize(width: 393, height: 4000))
        XCTAssertLessThanOrEqual(s.width, 393)
        XCTAssertGreaterThan(s.height, 110, "the chart and its words")
    }
}
