import MapLibre
import SwiftUI

/// Where one charge happened: the recorded position, the peak power (DC or not) and the network the server matched, from
/// the charge's row (`ChargeRow`, read once by the screen and shared with the session card). Held while the charge's
/// screen is open, never stored.
struct ChargeLocation: Decodable, Equatable {
    let lat: Double
    let lon: Double
    let maxPowerKW: Double?
    let network: String?
    /// The user had the pin placed where the truck was (#906): no snap to a charger. Not decoded here; from ChargeRow.
    var recorded = false

    private enum CodingKeys: String, CodingKey {
        case lat = "Lat", lon = "Lon", maxPowerKW = "MaxPowerKW", network = "Network"
    }

    /// The line under the map when the user placed the pin.
    static let recordedNote = "Pin placed where the truck was recorded."

    /// The web's rule for "may say DC fast charger": a peak of 30 kW or more.
    var isDC: Bool { (maxPowerKW ?? 0) >= 30 }

    /// A real fix (not 0,0 or out of range).
    var hasFix: Bool { lat.isFinite && lon.isFinite && !(lat == 0 && lon == 0) && abs(lat) <= 90 && abs(lon) <= 180 }
}

/// One charge's place on our own map, as the web's ChargeMap: the recorded position, snapped to the nearest charger
/// in the server's charger archive when that can't name the wrong network, with the charger's or the network's name.
/// The network is the ledger's for this charge (the month Money reads), else the charge row's own.
/// A picture like the drive map: no location services, nothing kept after the screen closes.
struct ChargeLocationMap: View {
    let chargeID: String
    /// When the charge started and on which truck: the ledger month that holds it.
    let startedAt: Date
    let vehicleID: String
    /// The charge's row, read by the screen (one request for this map and the session card).
    let row: ChargeRowModel.Phase
    var calendar: Calendar = .current

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    @State private var location: ChargeLocation?
    @State private var failed = false
    @State private var health = MapHealth.ok
    @State private var pinLabel: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 2 * Theme.unit) {
            map
            if let location, location.recorded, location.hasFix {
                Text(ChargeLocation.recordedNote).themeText(.caption, color: Theme.textSecondary)
            }
        }
        .task(id: LoadKey(chargeID: chargeID, ready: row.row != nil, failed: row == .failed)) { await load() }
    }

    private var map: some View {
        ZStack {
            if let location, !location.hasFix {
                MapMessage(text: "No location was recorded for this charge.")
            } else if let location, !failed, health != .failed, let base = MapStyle.json(dark: scheme == .dark, server: state.mapServer),
                      let style = MapStyle.withChargers(base, server: state.mapServer) {
                ChargeMapView(styleJSON: style, location: location, onHealth: { health = $0 }, onPin: { pinLabel = $0.label })
                    .allowsHitTesting(false)
                    .accessibilityElement()
                    .accessibilityLabel(pinLabel.map { "Map of where the charge happened: \($0)" } ?? "Map of where the charge happened")
                    .overlay(alignment: .bottomTrailing) { MapAttribution() }
                    .overlay { if health == .noTiles { MapUnavailableOverlay() } }
            } else if failed || health == .failed || location != nil {
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
    }

    private struct LoadKey: Equatable {
        let chargeID: String
        let ready: Bool
        let failed: Bool
    }

    private func load() async {
        MapNetwork.prepare(server: state.mapServer, token: state.mapToken)
        health = .ok
        pinLabel = nil
        switch row {
        case .loading:
            return
        case .failed:
            failed = true
        case .ready(let r):
            let client = state.client
            let f = DateFormatter()
            f.calendar = Calendar(identifier: .gregorian)
            f.timeZone = calendar.timeZone
            f.locale = Locale(identifier: "en_US_POSIX")
            f.dateFormat = "yyyy-MM"
            let month = f.string(from: startedAt)
            let ledger = try? await client.ledgerMonth(month: month, timeZone: calendar.timeZone.identifier, vehicleID: vehicleID)
            // The ledger's network wins (DA): a top-up takes its plug-in's.
            location = r.location(network: ledger.flatMap { ChargerPin.ledgerNetwork($0, chargeID: chargeID) })
            failed = false
        }
    }
}

extension ChargerPin.Pin {
    /// The words on the pin, if any.
    var label: String? {
        switch self {
        case .plain: return nil
        case .own(let label), .snapped(_, let label): return label
        }
    }
}

/// MapLibre's view of a charge: the pin (plain, labelled, or snapped with the recorded fix and a dashed link) at zoom
/// 16 on the charge. The charger archive's tiles come in with the style (MapStyle.withChargers); the nearest charger
/// is looked up once the map has settled on the charge.
struct ChargeMapView: UIViewRepresentable {
    let styleJSON: String
    let location: ChargeLocation
    var onHealth: (MapHealth) -> Void = { _ in }
    var onPin: (ChargerPin.Pin) -> Void = { _ in }

    /// The prefix of this map's own sources and layers (`clear` removes them); never a prefix of the charger
    /// archive's ids (MapStyle.chargerSourceID, "rivolt-chargers…"), which the style itself carries.
    static let sourceID = "rivolt-charge-pin"
    static let chargerImage = "rivolt-charge-pin-image"
    static let zoom = 16.0

    func makeCoordinator() -> Coordinator { Coordinator(location: location, onHealth: onHealth, onPin: onPin) }

    func makeUIView(context: Context) -> MLNMapView {
        MLNMapView.displayOnly(styleJSON: styleJSON, watch: context.coordinator)
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.setStyle(styleJSON, on: map)
    }

    final class Coordinator: MapLoadWatch {
        let location: ChargeLocation
        private let onPin: (ChargerPin.Pin) -> Void
        private(set) var pin = ChargerPin.Pin.plain
        /// The nearest-charger lookup ran (once, when the map first settled on the charge).
        private(set) var lookedUp = false
        private var points: MLNShapeSource?
        private var link: MLNShapeSource?

        init(location: ChargeLocation, onHealth: @escaping (MapHealth) -> Void, onPin: @escaping (ChargerPin.Pin) -> Void = { _ in }) {
            self.location = location
            self.onPin = onPin
            super.init(onHealth: onHealth)
        }

        private var here: CLLocationCoordinate2D { CLLocationCoordinate2D(latitude: location.lat, longitude: location.lon) }

        override func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {
            MapLoadWatch.clear(style, prefix: ChargeMapView.sourceID)
            style.setImage(Self.chargerSquare(), forName: ChargeMapView.chargerImage)
            let link = MLNShapeSource(identifier: "\(ChargeMapView.sourceID)-link", shape: nil, options: nil)
            let points = MLNShapeSource(identifier: "\(ChargeMapView.sourceID)-points", shape: nil, options: nil)
            style.addSource(link)
            style.addSource(points)

            let dashed = MLNLineStyleLayer(identifier: "\(ChargeMapView.sourceID)-link", source: link)
            dashed.lineColor = NSExpression(forConstantValue: UIColor(Theme.chargePin))
            dashed.lineWidth = NSExpression(forConstantValue: 1.5)
            dashed.lineOpacity = NSExpression(forConstantValue: 0.6)
            // The web's "4 4" at 1.5 px wide (MapLibre's dashes are in line widths).
            dashed.lineDashPattern = NSExpression(forConstantValue: [2.67, 2.67])
            style.addLayer(dashed)

            func dot(_ role: String, color: Color, radius: Double, stroke: Double, opacity: Double) -> MLNCircleStyleLayer {
                let c = MLNCircleStyleLayer(identifier: "\(ChargeMapView.sourceID)-\(role)", source: points)
                c.predicate = NSPredicate(format: "role == %@", role)
                c.circleColor = NSExpression(forConstantValue: UIColor(color))
                c.circleRadius = NSExpression(forConstantValue: radius)
                c.circleOpacity = NSExpression(forConstantValue: opacity)
                c.circleStrokeColor = NSExpression(forConstantValue: UIColor(Theme.chargePinInk))
                c.circleStrokeWidth = NSExpression(forConstantValue: stroke)
                return c
            }
            style.addLayer(dot("plain", color: Theme.chargePin, radius: 7, stroke: 2, opacity: 1))
            style.addLayer(dot("recorded", color: Theme.chargeRecorded, radius: 4, stroke: 1.5, opacity: 0.85))

            let charger = MLNSymbolStyleLayer(identifier: "\(ChargeMapView.sourceID)-charger", source: points)
            charger.predicate = NSPredicate(format: "role == %@", "charger")
            charger.iconImageName = NSExpression(forConstantValue: ChargeMapView.chargerImage)
            charger.iconAllowsOverlap = NSExpression(forConstantValue: true)
            charger.text = NSExpression(forKeyPath: "label")
            charger.textFontNames = NSExpression(forConstantValue: ["Noto Sans Medium"])
            charger.textFontSize = NSExpression(forConstantValue: 12)
            charger.textColor = NSExpression(forConstantValue: UIColor(Theme.textPrimary))
            charger.textHaloColor = NSExpression(forConstantValue: UIColor(Theme.background))
            charger.textHaloWidth = NSExpression(forConstantValue: 1.5)
            charger.textAnchor = NSExpression(forConstantValue: "bottom")
            charger.textOffset = NSExpression(forConstantValue: NSValue(cgVector: CGVector(dx: 0, dy: -1.2)))
            charger.textAllowsOverlap = NSExpression(forConstantValue: true)
            style.addLayer(charger)

            self.link = link
            self.points = points
            draw()
            center(map)
        }

        /// Shows `p` (the lookup's answer, or a test's).
        func apply(_ p: ChargerPin.Pin) {
            pin = p
            draw()
            DispatchQueue.main.async { self.onPin(p) }
        }

        private func draw() {
            func point(_ c: CLLocationCoordinate2D, _ role: String, _ label: String? = nil) -> MLNPointFeature {
                let f = MLNPointFeature()
                f.coordinate = c
                f.attributes = label.map { ["role": role, "label": $0] } ?? ["role": role]
                return f
            }
            switch pin {
            case .plain:
                points?.shape = MLNShapeCollectionFeature(shapes: [point(here, "plain")])
                link?.shape = nil
            case .own(let label):
                points?.shape = MLNShapeCollectionFeature(shapes: [point(here, "charger", label)])
                link?.shape = nil
            case .snapped(let node, let label):
                let at = CLLocationCoordinate2D(latitude: node.lat, longitude: node.lon)
                points?.shape = MLNShapeCollectionFeature(shapes: [point(here, "recorded"), point(at, "charger", label)])
                var ends = [here, at]
                link?.shape = MLNPolylineFeature(coordinates: &ends, count: 2)
            }
        }

        override func resized(_ map: MLNMapView) { center(map) }

        /// The charge at zoom 16, as the web (once the view has a size: MapLibre ignores a camera set before).
        private func center(_ map: MLNMapView) {
            guard map.bounds.width > 0, map.bounds.height > 0 else { return }
            map.setCenter(here, zoomLevel: ChargeMapView.zoom, animated: false)
        }

        /// Looks the nearest charger up once the map has settled on the charge, so the archive's tiles around it are in.
        override func mapViewDidBecomeIdle(_ map: MLNMapView) {
            super.mapViewDidBecomeIdle(map)
            guard !lookedUp, let style = map.style, map.bounds.width > 0, map.zoomLevel >= ChargeMapView.zoom - 1 else { return }
            lookedUp = true
            // The user's own pin (#906): the recorded position, no charger lookup.
            if location.recorded {
                apply(.plain)
                return
            }
            apply(ChargerPin.pin(snapped: Self.nearestCharger(in: style, lat: location.lat, lon: location.lon),
                                 network: location.network, isDC: location.isDC))
        }

        /// The archive's nearest charger, else the basemap's nearest charging station.
        static func nearestCharger(in style: MLNStyle, lat: Double, lon: Double) -> ChargerPin.Node? {
            func candidates(_ source: String, _ layer: String) -> [ChargerPin.Candidate] {
                guard let s = style.source(withIdentifier: source) as? MLNVectorTileSource else { return [] }
                return s.features(sourceLayerIdentifiers: [layer], predicate: nil).compactMap { f in
                    guard let p = f as? MLNPointFeature else { return nil }
                    return ChargerPin.Candidate(lat: p.coordinate.latitude, lon: p.coordinate.longitude, props: p.attributes)
                }
            }
            return ChargerPin.nearest(candidates(MapStyle.chargerSourceID, "chargers"), lat: lat, lon: lon, archive: true)
                ?? ChargerPin.nearest(candidates("protomaps", "pois"), lat: lat, lon: lon, archive: false)
        }

        /// The web's charger pin: an amber rounded square with a dark ring and bolt.
        static func chargerSquare() -> UIImage {
            let side: CGFloat = 22
            return UIGraphicsImageRenderer(size: CGSize(width: side, height: side)).image { _ in
                let path = UIBezierPath(roundedRect: CGRect(x: 1, y: 1, width: side - 2, height: side - 2), cornerRadius: 6)
                UIColor(Theme.chargePin).setFill()
                path.fill()
                UIColor(Theme.chargePinInk).setStroke()
                path.lineWidth = 2
                path.stroke()
                let config = UIImage.SymbolConfiguration(pointSize: 11, weight: .bold)
                if let bolt = UIImage(systemName: "bolt.fill", withConfiguration: config)?
                    .withTintColor(UIColor(Theme.chargePinInk), renderingMode: .alwaysOriginal) {
                    bolt.draw(at: CGPoint(x: (side - bolt.size.width) / 2, y: (side - bolt.size.height) / 2))
                }
            }
        }
    }
}
