import MapLibre
import SwiftUI

/// One drive's route on the map: the server's basemap (our own tiles, light or dark with the app) and the recorded
/// trace in the accent colour, fitted to the screen. Fetched when shown, kept in memory while the screen is open,
/// gone with it. No location services: the map never shows or asks where the phone is.
struct DriveRouteMap: View {
    let driveID: String

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    @State private var route: [DriveRoute.Point]?
    @State private var failed = false
    /// The basemap couldn't load (no tiles, or the map itself failed) after the drive's route arrived.
    @State private var mapFailed = false

    var body: some View {
        ZStack {
            if let route, route.isEmpty {
                MapMessage(text: "No route was recorded for this drive.")
            } else if let route, !failed, !mapFailed, let style = MapStyle.json(dark: scheme == .dark, server: state.serverURL) {
                // A picture of the route, not a map to explore: it never takes the detail screen's scrolling.
                RouteMapView(styleJSON: style, route: route, onFail: { mapFailed = true })
                    .allowsHitTesting(false)
                    .accessibilityElement()
                    .accessibilityLabel("Map of the drive route")
                    .overlay(alignment: .bottomTrailing) { MapAttribution() }
            } else if failed || mapFailed || route != nil {
                // The drive or the basemap couldn't load, or the bundled style is missing: never an endless spinner.
                MapMessage(text: "Map unavailable.")
            } else {
                ProgressView()
            }
        }
        .frame(maxWidth: .infinity)
        .frame(height: 220)
        .background(Theme.card)
        .clipShape(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .task(id: driveID) { await load() }
    }

    private func load() async {
        MapNetwork.prepare(server: state.serverURL, token: try? state.tokens.token(for: state.serverURL))
        mapFailed = false
        do {
            route = try await state.client.driveRoute(id: driveID).points
            failed = false
        } catch {
            failed = true
        }
    }
}

/// MapLibre's view: the style (as JSON, never fetched), the route as one shape source and line layer, no user
/// location, no rotation or tilt.
struct RouteMapView: UIViewRepresentable {
    let styleJSON: String
    let route: [DriveRoute.Point]
    var onFail: () -> Void = {}

    static let sourceID = "rivolt-route"

    func makeCoordinator() -> Coordinator { Coordinator(route: route, onFail: onFail) }

    func makeUIView(context: Context) -> MLNMapView {
        let map = MLNMapView.displayOnly(styleJSON: styleJSON)
        map.delegate = context.coordinator
        return map
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.route = route
        if map.styleJSON != styleJSON { map.styleJSON = styleJSON }
    }

    final class Coordinator: MapLoadWatch {
        var route: [DriveRoute.Point]

        init(route: [DriveRoute.Point], onFail: @escaping () -> Void) {
            self.route = route
            super.init(onFail: onFail)
        }

        override func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {
            var coords = route.map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) }
            guard coords.count >= 2 else { return }
            let line = MLNPolylineFeature(coordinates: &coords, count: UInt(coords.count))
            let source = MLNShapeSource(identifier: RouteMapView.sourceID, shape: line, options: nil)
            style.addSource(source)
            let layer = MLNLineStyleLayer(identifier: RouteMapView.sourceID, source: source)
            layer.lineColor = NSExpression(forConstantValue: UIColor(Theme.accent))
            layer.lineWidth = NSExpression(forConstantValue: 4)
            layer.lineCap = NSExpression(forConstantValue: "round")
            layer.lineJoin = NSExpression(forConstantValue: "round")
            style.addLayer(layer)
            map.setVisibleCoordinates(&coords, count: UInt(coords.count), edgePadding: UIEdgeInsets(top: 24, left: 24, bottom: 24, right: 24), animated: false)
        }
    }
}

/// The tiles are OpenStreetMap data (ODbL) through Protomaps: credited on every map, as the web does.
struct MapAttribution: View {
    var body: some View {
        Text("© OpenStreetMap · Protomaps")
            .font(Theme.sans(ThemeTokens.typeCaption - 1, .regular, relativeTo: .caption2))
            .foregroundStyle(Theme.textSecondary)
            .padding(.horizontal, 4).padding(.vertical, 2)
            .background(Theme.background.opacity(0.75), in: RoundedRectangle(cornerRadius: 3))
            .padding(4)
            .accessibilityLabel("Map data OpenStreetMap, style Protomaps")
    }
}

extension MLNMapView {
    /// A map that is a picture: our style (as JSON, never fetched), no user location, no pan, zoom, rotation or tilt,
    /// no compass, and the credit drawn by the SwiftUI side (MapAttribution) instead of the info button.
    static func displayOnly(styleJSON: String) -> MLNMapView {
        let map = MLNMapView(frame: .zero, styleJSON: styleJSON)
        map.showsUserLocation = false
        map.isRotateEnabled = false
        map.isPitchEnabled = false
        map.isScrollEnabled = false
        map.isZoomEnabled = false
        map.compassView.isHidden = true
        map.attributionButton.isHidden = true
        map.logoViewPosition = .bottomLeft
        return map
    }
}

/// A quiet line in a map's place when there is nothing to draw.
struct MapMessage: View {
    let text: String
    var body: some View { Text(text).themeText(.subline, color: Theme.textTertiary) }
}

/// What both maps' MapLibre delegates share: the style's arrival (`styleLoaded`), and telling the view the basemap
/// failed, either the map itself (style or connection) or every tile request when it settles with none loaded.
class MapLoadWatch: NSObject, MLNMapViewDelegate {
    private let onFail: () -> Void
    private var tilesLoaded = 0
    private var tileErrors = 0
    private var reported = false

    init(onFail: @escaping () -> Void) { self.onFail = onFail }

    func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {}

    func mapView(_ map: MLNMapView, didFinishLoading style: MLNStyle) { styleLoaded(map, style) }

    func mapViewDidFailLoadingMap(_ map: MLNMapView, withError error: Error) { fail() }

    func mapView(_ map: MLNMapView, tileDidTriggerAction operation: MLNTileOperation, x: Int, y: Int, z: Int, wrap: Int,
                 overscaledZ: Int, sourceID: String) {
        switch operation {
        case .loadFromNetwork, .loadFromCache: tilesLoaded += 1
        case .error: tileErrors += 1
        default: break
        }
    }

    func mapViewDidBecomeIdle(_ map: MLNMapView) {
        if tileErrors > 0 && tilesLoaded == 0 { fail() }
    }

    private func fail() {
        guard !reported else { return }
        reported = true
        DispatchQueue.main.async { self.onFail() }
    }
}
