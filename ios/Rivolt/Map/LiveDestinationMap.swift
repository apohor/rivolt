import MapLibre
import SwiftUI

/// Where the truck's navigation is going (design_preview key live_destination), on our own map as the web's Live map draws it: the planned route
/// as a dashed line and a pin at the destination, the camera on the whole of it. A picture, not a map to explore; no location services; the
/// route and the place are held while the screen shows them and never stored.
struct LiveDestinationMap: View {
    let destination: LiveDestination

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    @State private var health = MapHealth.ok
    @State private var prepared = false

    var body: some View {
        ZStack {
            if health == .failed {
                MapMessage(text: "Map unavailable.")
            } else if prepared, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                LiveDestinationMapView(styleJSON: style, destination: destination, onHealth: { health = $0 })
                    .allowsHitTesting(false)
                    .accessibilityElement()
                    .accessibilityLabel("Map of the route to \(destination.name)")
                    .overlay(alignment: .bottomTrailing) { MapAttribution() }
                    .overlay { if health == .noTiles { MapUnavailableOverlay() } }
            } else {
                ProgressView()
            }
        }
        .frame(maxWidth: .infinity)
        .frame(height: 220)
        .background(Theme.card)
        .clipShape(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .accessibilityIdentifier("live-destination-map")
        .task {
            MapNetwork.prepare(server: state.mapServer, token: state.mapToken)
            prepared = true
        }
    }
}

/// MapLibre's view of the destination: the dashed route under the pin, framed once per destination.
struct LiveDestinationMapView: UIViewRepresentable {
    let styleJSON: String
    let destination: LiveDestination
    var onHealth: (MapHealth) -> Void = { _ in }

    /// The prefix of this map's own sources and layers (`clear` removes them).
    static let sourceID = "rivolt-live-destination"

    func makeCoordinator() -> Coordinator { Coordinator(destination: destination, onHealth: onHealth) }

    func makeUIView(context: Context) -> MLNMapView {
        MLNMapView.displayOnly(styleJSON: styleJSON, watch: context.coordinator)
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.setStyle(styleJSON, on: map)
        context.coordinator.update(destination, on: map)
    }

    final class Coordinator: MapLoadWatch {
        private var destination: LiveDestination
        private var line: MLNShapeSource?
        private var pin: MLNShapeSource?
        /// The destination the camera was last fitted to (a new route to the same place does not move it).
        private var fittedTo: String?

        init(destination: LiveDestination, onHealth: @escaping (MapHealth) -> Void) {
            self.destination = destination
            super.init(onHealth: onHealth)
        }

        override func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {
            MapLoadWatch.clear(style, prefix: LiveDestinationMapView.sourceID)
            let line = MLNShapeSource(identifier: "\(LiveDestinationMapView.sourceID)-line", shape: nil, options: nil)
            let pin = MLNShapeSource(identifier: "\(LiveDestinationMapView.sourceID)-pin", shape: nil, options: nil)
            style.addSource(line)
            style.addSource(pin)

            let dashed = MLNLineStyleLayer(identifier: "\(LiveDestinationMapView.sourceID)-line", source: line)
            dashed.lineColor = NSExpression(forConstantValue: UIColor(red: 0.22, green: 0.74, blue: 0.97, alpha: 1)) // the web's #38bdf8
            dashed.lineWidth = NSExpression(forConstantValue: 3.5)
            dashed.lineOpacity = NSExpression(forConstantValue: 0.85)
            dashed.lineDashPattern = NSExpression(forConstantValue: [2, 2])
            style.addLayer(dashed)

            let dot = MLNCircleStyleLayer(identifier: "\(LiveDestinationMapView.sourceID)-pin", source: pin)
            dot.circleColor = NSExpression(forConstantValue: UIColor(red: 0.96, green: 0.25, blue: 0.37, alpha: 1)) // the web's #f43f5e
            dot.circleRadius = NSExpression(forConstantValue: 7)
            dot.circleStrokeColor = NSExpression(forConstantValue: UIColor(red: 0.04, green: 0.04, blue: 0.04, alpha: 1))
            dot.circleStrokeWidth = NSExpression(forConstantValue: 2)
            style.addLayer(dot)

            self.line = line
            self.pin = pin
            draw()
            fit(map)
        }

        func update(_ d: LiveDestination, on map: MLNMapView) {
            guard d != destination else { return }
            destination = d
            draw()
            fit(map)
        }

        override func resized(_ map: MLNMapView) { fit(map) }

        private var here: CLLocationCoordinate2D { CLLocationCoordinate2D(latitude: destination.lat, longitude: destination.lon) }

        private func draw() {
            let f = MLNPointFeature()
            f.coordinate = here
            pin?.shape = f
            var coords = destination.routePoints.map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) }
            line?.shape = coords.count >= 2 ? MLNPolylineFeature(coordinates: &coords, count: UInt(coords.count)) : nil
        }

        /// The route and the pin in view, once the view has a size (MapLibre ignores a camera set before) and once per destination.
        private func fit(_ map: MLNMapView) {
            guard map.bounds.width > 0, map.bounds.height > 0 else { return }
            let key = "\(destination.lat),\(destination.lon)"
            guard fittedTo != key else { return }
            fittedTo = key
            var coords = destination.routePoints.map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) } + [here]
            if coords.count >= 2 {
                map.setVisibleCoordinates(&coords, count: UInt(coords.count), edgePadding: UIEdgeInsets(top: 32, left: 32, bottom: 32, right: 32), animated: false)
            } else {
                map.setCenter(here, zoomLevel: 14, animated: false)
            }
        }
    }
}
