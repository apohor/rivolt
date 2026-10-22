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

    var body: some View {
        ZStack {
            if let route, !route.isEmpty, let style = MapStyle.json(dark: scheme == .dark, server: state.serverURL) {
                // A picture of the route, not a map to explore: it never takes the detail screen's scrolling.
                RouteMapView(styleJSON: style, route: route, dark: scheme == .dark)
                    .allowsHitTesting(false)
                    .accessibilityElement()
                    .accessibilityLabel("Map of the drive route")
                    .overlay(alignment: .bottomTrailing) { attribution }
            } else if failed || route?.isEmpty == true {
                Text(route?.isEmpty == true ? "No route was recorded for this drive." : "Map unavailable.")
                    .themeText(.subline, color: Theme.textTertiary)
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

    /// The tiles are OpenStreetMap data (ODbL) through Protomaps: credited on the map, as the web does.
    private var attribution: some View {
        Text("© OpenStreetMap · Protomaps")
            .font(Theme.sans(ThemeTokens.typeCaption - 1, .regular, relativeTo: .caption2))
            .foregroundStyle(Theme.textSecondary)
            .padding(.horizontal, 4).padding(.vertical, 2)
            .background(Theme.background.opacity(0.75), in: RoundedRectangle(cornerRadius: 3))
            .padding(4)
            .accessibilityLabel("Map data OpenStreetMap, style Protomaps")
    }

    private func load() async {
        MapNetwork.prepare(server: state.serverURL, token: try? state.tokens.token(for: state.serverURL))
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
    let dark: Bool

    static let sourceID = "rivolt-route"

    func makeCoordinator() -> Coordinator { Coordinator(route: route) }

    func makeUIView(context: Context) -> MLNMapView {
        let map = MLNMapView(frame: .zero, styleJSON: styleJSON)
        map.delegate = context.coordinator
        map.showsUserLocation = false
        map.isRotateEnabled = false
        map.isPitchEnabled = false
        map.isScrollEnabled = false
        map.isZoomEnabled = false
        map.compassView.isHidden = true
        // The credit is drawn as text over the map (DriveRouteMap), visible without a tap.
        map.attributionButton.isHidden = true
        map.logoViewPosition = .bottomLeft
        return map
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.route = route
        if map.styleJSON != styleJSON { map.styleJSON = styleJSON }
    }

    final class Coordinator: NSObject, MLNMapViewDelegate {
        var route: [DriveRoute.Point]

        init(route: [DriveRoute.Point]) { self.route = route }

        func mapView(_ map: MLNMapView, didFinishLoading style: MLNStyle) {
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
