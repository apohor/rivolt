import MapLibre
import XCTest
@testable import Rivolt

/// The maps as SwiftUI builds them: created at zero size with the style as JSON, laid out later. 0.2.6 drew neither
/// the route nor the pins and left the camera on the whole world, because MapLibre finished loading a JSON style
/// inside its initialiser, before there was a delegate to add them.
@MainActor
final class MapCameraTests: XCTestCase {
    private let server = URL(string: "https://rivolt.example")!

    /// Waits for the style (MapLibre may load a JSON style at once or on a later turn of the run loop).
    private func styleIn(_ map: MLNMapView, file: StaticString = #filePath, line: UInt = #line) throws -> MLNStyle {
        let deadline = Date().addingTimeInterval(5)
        while map.style == nil, Date() < deadline { RunLoop.main.run(until: Date().addingTimeInterval(0.05)) }
        return try XCTUnwrap(map.style, "the style never loaded", file: file, line: line)
    }

    private func layOut(_ map: MLNMapView) {
        map.frame = CGRect(x: 0, y: 0, width: 360, height: 220)
        map.layoutIfNeeded()
    }

    func testADriveRouteIsDrawnAndTheCameraFitsIt() throws {
        let a = RouteSnap.Coord(lat: 32.7767, lon: -96.7970), b = RouteSnap.Coord(lat: 32.8500, lon: -96.8000)
        let drawing = RouteSnap.Drawing(pieces: [RouteSnap.Piece(coords: [a, b], speed: .to50)], routedGaps: [], gaps: [],
                                        start: a, end: b, hasSpeed: true)
        let style = try XCTUnwrap(MapStyle.json(dark: false, server: server))
        let coordinator = RouteMapView.Coordinator(drawing: drawing, onHealth: { _ in })
        let map = MLNMapView.displayOnly(styleJSON: style, watch: coordinator)
        XCTAssertIdentical(map.delegate, coordinator)
        XCTAssertTrue(map.logoView.isHidden)

        let loaded = try styleIn(map)
        for name in ["pieces", "routed", "gaps", "pins"] {
            XCTAssertNotNil(loaded.source(withIdentifier: "\(RouteMapView.sourceID)-\(name)"), name)
        }
        XCTAssertNotNil(loaded.layer(withIdentifier: "\(RouteMapView.sourceID)-pin-start"))
        XCTAssertNotNil(loaded.layer(withIdentifier: "\(RouteMapView.sourceID)-speed\(RouteSnap.Speed.to50.rawValue)"))

        // The fit made before layout is redone once the view has its size: on the drive, not the world.
        layOut(map)
        XCTAssertGreaterThan(map.zoomLevel, 9)
        XCTAssertEqual(map.centerCoordinate.latitude, 32.81, accuracy: 0.05)
        XCTAssertEqual(map.centerCoordinate.longitude, -96.80, accuracy: 0.05)

        // The snapped drawing arriving later moves the camera onto it too.
        let c = RouteSnap.Coord(lat: 30.2672, lon: -97.7431), d = RouteSnap.Coord(lat: 30.3000, lon: -97.7000)
        coordinator.update(RouteSnap.Drawing(pieces: [RouteSnap.Piece(coords: [c, d], speed: nil)], routedGaps: [], gaps: [],
                                             start: c, end: d, hasSpeed: false), on: map)
        XCTAssertEqual(map.centerCoordinate.latitude, 30.28, accuracy: 0.05)

        // A new style (dark) gets the route again, once.
        coordinator.setStyle(try XCTUnwrap(MapStyle.json(dark: true, server: server)), on: map)
        let dark = try styleIn(map)
        XCTAssertNotNil(dark.source(withIdentifier: "\(RouteMapView.sourceID)-pieces"))
        XCTAssertEqual(dark.layers.filter { $0.identifier.hasPrefix("\(RouteMapView.sourceID)-pin-") }.count, 2)
    }

    /// The owner's call: only "© OpenStreetMap" on the map (ODbL); Protomaps is credited, with its licence, in
    /// Acknowledgements, which ship the licence file.
    func testTheMapCreditIsOpenStreetMapAndProtomapsIsInAcknowledgements() throws {
        XCTAssertEqual(MapAttribution.text, "© OpenStreetMap")
        XCTAssertNotNil(Bundle.main.url(forResource: "LICENSE-protomaps-basemaps", withExtension: "txt", subdirectory: "Map"))
    }

    func testTheRouteSaysWhenItIsDrawn() throws {
        let a = RouteSnap.Coord(lat: 32.7767, lon: -96.7970), b = RouteSnap.Coord(lat: 32.8500, lon: -96.8000)
        let drawn = expectation(description: "drawn")
        let coordinator = RouteMapView.Coordinator(
            drawing: RouteSnap.Drawing(pieces: [RouteSnap.Piece(coords: [a, b], speed: .to25)], routedGaps: [], gaps: [], start: a, end: b, hasSpeed: true),
            onHealth: { _ in }, onDrawn: { drawn.fulfill() })
        let map = MLNMapView.displayOnly(styleJSON: try XCTUnwrap(MapStyle.json(dark: false, server: server)), watch: coordinator)
        _ = try styleIn(map)
        wait(for: [drawn], timeout: 5)
    }

    func testChargePlacesAreDrawnAndTheCameraFitsThem() throws {
        let places = [
            ChargePlace(label: "Home", lat: 32.7767, lon: -96.7970, sessions: 12, energyKwh: 300, radiusM: 50),
            ChargePlace(label: "Fast", lat: 32.9000, lon: -96.9000, sessions: 2, energyKwh: 90, radiusM: 50),
        ]
        let style = try XCTUnwrap(MapStyle.json(dark: false, server: server))
        let coordinator = ChargePlacesMapView.Coordinator(places: places, onHealth: { _ in })
        let map = MLNMapView.displayOnly(styleJSON: style, watch: coordinator)
        let loaded = try styleIn(map)
        XCTAssertNotNil(loaded.source(withIdentifier: ChargePlacesMapView.sourceID))
        layOut(map)
        XCTAssertGreaterThan(map.zoomLevel, 7)
        XCTAssertEqual(map.centerCoordinate.latitude, 32.84, accuracy: 0.05)
    }

    func testAChargeIsCentredAndItsPinDrawn() throws {
        let location = ChargeLocation(lat: 32.7767, lon: -96.7970, maxPowerKW: 150, network: "Electrify America")
        let style = try XCTUnwrap(MapStyle.withChargers(try XCTUnwrap(MapStyle.json(dark: false, server: server)), server: server))
        let coordinator = ChargeMapView.Coordinator(location: location, onHealth: { _ in })
        let map = MLNMapView.displayOnly(styleJSON: style, watch: coordinator)
        let loaded = try styleIn(map)
        XCTAssertNotNil(loaded.source(withIdentifier: MapStyle.chargerSourceID) as? MLNVectorTileSource)
        XCTAssertNotNil(loaded.layer(withIdentifier: MapStyle.chargerProbeLayerID))
        XCTAssertFalse(MapStyle.chargerSourceID.hasPrefix(ChargeMapView.sourceID), "clear() would remove the archive")
        let points = try XCTUnwrap(loaded.source(withIdentifier: "\(ChargeMapView.sourceID)-points") as? MLNShapeSource)
        XCTAssertEqual((points.shape as? MLNShapeCollectionFeature)?.shapes.count, 1)
        XCTAssertNotNil(loaded.image(forName: ChargeMapView.chargerImage))

        layOut(map)
        XCTAssertEqual(map.zoomLevel, ChargeMapView.zoom, accuracy: 0.01)
        XCTAssertEqual(map.centerCoordinate.latitude, 32.7767, accuracy: 0.001)

        // Snapped: the recorded fix and the charger, joined by the dashed link.
        let node = ChargerPin.Node(lat: 32.7780, lon: -96.7965, name: "Electrify America", network: "Electrify America", distanceM: 150)
        coordinator.apply(.snapped(node, label: "Electrify America"))
        let shapes = try XCTUnwrap((points.shape as? MLNShapeCollectionFeature)?.shapes as? [MLNPointFeature])
        XCTAssertEqual(shapes.compactMap { $0.attributes["role"] as? String }, ["recorded", "charger"])
        XCTAssertEqual(shapes[1].attributes["label"] as? String, "Electrify America")
        let link = try XCTUnwrap(loaded.source(withIdentifier: "\(ChargeMapView.sourceID)-link") as? MLNShapeSource)
        XCTAssertNotNil(link.shape as? MLNPolylineFeature)
    }
}
