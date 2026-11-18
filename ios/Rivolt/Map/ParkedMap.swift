import MapLibre
import SwiftUI

/// Where the truck is (design_preview key now_slot), on our own map: a pin on the spot, a picture rather than a map to explore. No location
/// services; the position is held while the screen shows it and never stored. The demo's point is a public sample and says so.
struct ParkedMap: View {
    let lat: Double
    let lon: Double
    let label: String?

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    @State private var health = MapHealth.ok
    @State private var prepared = false

    var body: some View {
        ZStack {
            if health == .failed {
                MapMessage(text: "Map unavailable.")
            } else if prepared, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                ParkedMapView(styleJSON: style, lat: lat, lon: lon, onHealth: { health = $0 })
                    .allowsHitTesting(false)
                    .overlay(alignment: .bottomTrailing) { MapAttribution() }
                    .overlay { if health == .noTiles { MapUnavailableOverlay() } }
                    .overlay(alignment: .topLeading) {
                        if let label {
                            Text(label).themeText(.caption, color: Theme.textPrimary)
                                .padding(.horizontal, 8).padding(.vertical, 4)
                                .background(Theme.card.opacity(0.9), in: Capsule())
                                .padding(8)
                        }
                    }
                    .overlay(alignment: .bottomLeading) {
                        if state.isDemo {
                            Text("Sample map, not a real location").themeText(.caption, color: Theme.textSecondary)
                                .padding(.horizontal, 6).padding(.vertical, 2)
                                .background(Theme.card.opacity(0.9), in: Capsule())
                                .padding(6)
                        }
                    }
            } else {
                ProgressView()
            }
        }
        .frame(maxWidth: .infinity)
        .frame(height: 170)
        .background(Theme.card)
        .clipShape(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .accessibilityHidden(true)
        .accessibilityIdentifier("now-slot-map")
        .task {
            MapNetwork.prepare(server: state.mapServer, token: state.mapToken)
            prepared = true
        }
    }
}

/// MapLibre's view of the spot: a pin, the camera centred on it; it follows the truck when the position changes.
struct ParkedMapView: UIViewRepresentable {
    let styleJSON: String
    let lat: Double
    let lon: Double
    var onHealth: (MapHealth) -> Void = { _ in }

    static let sourceID = "rivolt-parked"

    func makeCoordinator() -> Coordinator { Coordinator(spot: CLLocationCoordinate2D(latitude: lat, longitude: lon), onHealth: onHealth) }

    func makeUIView(context: Context) -> MLNMapView {
        MLNMapView.displayOnly(styleJSON: styleJSON, watch: context.coordinator)
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.setStyle(styleJSON, on: map)
        context.coordinator.update(CLLocationCoordinate2D(latitude: lat, longitude: lon), on: map)
    }

    final class Coordinator: MapLoadWatch {
        private var spot: CLLocationCoordinate2D
        private var pin: MLNShapeSource?

        init(spot: CLLocationCoordinate2D, onHealth: @escaping (MapHealth) -> Void) {
            self.spot = spot
            super.init(onHealth: onHealth)
        }

        override func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {
            MapLoadWatch.clear(style, prefix: ParkedMapView.sourceID)
            let source = MLNShapeSource(identifier: "\(ParkedMapView.sourceID)-pin", shape: nil, options: nil)
            style.addSource(source)
            let dot = MLNCircleStyleLayer(identifier: "\(ParkedMapView.sourceID)-pin", source: source)
            dot.circleColor = NSExpression(forConstantValue: UIColor(red: 0.96, green: 0.25, blue: 0.37, alpha: 1)) // the web's #f43f5e
            dot.circleRadius = NSExpression(forConstantValue: 7)
            dot.circleStrokeColor = NSExpression(forConstantValue: UIColor(red: 0.04, green: 0.04, blue: 0.04, alpha: 1))
            dot.circleStrokeWidth = NSExpression(forConstantValue: 2)
            style.addLayer(dot)
            pin = source
            draw()
            center(map)
        }

        func update(_ next: CLLocationCoordinate2D, on map: MLNMapView) {
            guard next.latitude != spot.latitude || next.longitude != spot.longitude else { return }
            spot = next
            draw()
            center(map)
        }

        override func resized(_ map: MLNMapView) { center(map) }

        private func draw() {
            let f = MLNPointFeature()
            f.coordinate = spot
            pin?.shape = f
        }

        /// Once the view has a size (MapLibre ignores a camera set before).
        private func center(_ map: MLNMapView) {
            guard map.bounds.width > 0, map.bounds.height > 0 else { return }
            map.setCenter(spot, zoomLevel: 14, animated: false)
        }
    }
}
