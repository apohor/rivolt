import SwiftUI
import XCTest
@testable import Rivolt

@MainActor
final class PlanViasTests: XCTestCase {
    private let line = (0..<10).map { RouteSnap.Coord(lat: 40 + Double($0) * 0.1, lon: -100) }

    func testTheRequestPutsTheViasBetweenTheTwoEnds() {
        let vias = [PlanVia(lat: 40.3, lon: -100, name: "A"), PlanVia(lat: 40.6, lon: -100, name: "B", entityID: "e2")]
        let r = TripPlanLogic.request(vehicleID: "v", origin: (40, -100), destination: (41, -100), startingSoc: nil, departAt: nil, packKwh: nil, vias: vias)
        XCTAssertEqual(r.waypoints.map(\.waypointType), ["origin", "waypoint", "waypoint", "destination"])
        XCTAssertEqual(r.waypoints[2].entityId, "e2")
        XCTAssertEqual(TripPlanLogic.request(vehicleID: "v", origin: (40, -100), destination: (41, -100), startingSoc: nil, departAt: nil, packKwh: nil).waypoints.count, 2)
    }

    func testTheViasGoInTheOrderTheRouteReachesThem() {
        let a = PlanVia(lat: 40.7, lon: -100, name: "far"), b = PlanVia(lat: 40.2, lon: -100, name: "near")
        XCTAssertEqual(TripPlanLogic.orderedVias([a, b], along: line).map(\.name), ["near", "far"])
        XCTAssertEqual(TripPlanLogic.orderedVias([a, b], along: []).map(\.name), ["far", "near"], "no line: the order they were added")
    }

    func testHowFarOffTheRoute() throws {
        let on = try XCTUnwrap(TripPlanLogic.offRouteMeters(lat: 40.3, lon: -100, line: line))
        XCTAssertLessThan(on, 5)
        let off = try XCTUnwrap(TripPlanLogic.offRouteMeters(lat: 40.3, lon: -99.95, line: line))
        XCTAssertEqual(off, 4_250, accuracy: 250)
        XCTAssertNil(TripPlanLogic.offRouteMeters(lat: 40, lon: -100, line: []))
        XCTAssertEqual(TripPlanLogic.offRouteLine(on, metric: false), "right on the route")
        XCTAssertTrue(TripPlanLogic.offRouteLine(off, metric: false)?.hasPrefix("about ") == true)
        XCTAssertNil(TripPlanLogic.offRouteLine(nil, metric: false))
    }

    func testTheCalloutSaysPowerNetworkAndDetourButNeverFreeOrWorking() {
        let c = AlongCharger(lat: 1, lon: 2, name: "Sample Fast Charge", isDCFC: true, maxPowerKW: 250, evNetwork: "Example Network")
        let text = PlanSheetLogic.chargerDetail(c, offRoute: "about 2 mi off the route")
        XCTAssertEqual(text, "250 kW · Example Network · about 2 mi off the route")
        XCTAssertNil(text.range(of: "free|working|available|stalls", options: [.regularExpression, .caseInsensitive]))
        XCTAssertTrue(PlanSheetLogic.passThrough.contains("doesn't promise a charge"))
        XCTAssertEqual(PlanSheetLogic.chargerDetail(AlongCharger(lat: 1, lon: 2), offRoute: nil), "Fast charger")
    }

    /// The callout and the layer chip as they draw, light and dark, default and a large text size.
    func testRender() throws {
        Theme.registerFonts()
        let c = AlongCharger(lat: 1, lon: 2, name: "Sample Fast Charge", isDCFC: true, maxPowerKW: 250, evNetwork: "Example Network")
        for scheme in [ColorScheme.light, .dark] {
            for (label, type) in [("default", DynamicTypeSize.large), ("ax3", .accessibility3)] {
                let page = VStack(alignment: .leading, spacing: 12) {
                    Text("Fast chargers · 12").themeText(.subline, color: Theme.textPrimary).padding(.horizontal, 12).frame(minHeight: 36)
                        .background(Theme.borderStrong, in: Capsule())
                    PlanPinCallout(charger: c, offRoute: "about 2 mi off the route", adding: false, onAdd: {}, onClose: {})
                }
                .padding(12).frame(width: 367).background(ThemePageBackground())
                let renderer = ImageRenderer(content: page.environment(\.colorScheme, scheme).environment(\.dynamicTypeSize, type))
                renderer.scale = 2
                let png = try XCTUnwrap(try XCTUnwrap(renderer.uiImage, "callout did not render").pngData())
                let file = "plan-callout-\(scheme == .dark ? "dark" : "light")-\(label).png"
                let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
                attachment.name = file
                attachment.lifetime = .keepAlways
                add(attachment)
                if let dir = ProcessInfo.processInfo.environment["SNAPSHOT_DIR"], !dir.isEmpty {
                    try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                    try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file))
                }
            }
        }
    }
}
