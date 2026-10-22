import MapLibre
import SwiftUI

/// One place the account charges, from `GET /api/charges/clusters` (all trucks, all time): the server's label, the
/// centre of the cluster, and how much charging happened there. The member charge ids are not decoded. Held in memory
/// only, like a drive's route.
struct ChargePlace: Decodable, Equatable, Identifiable {
    /// "Home", "Public", "Fast", or "" when the server couldn't say.
    let label: String
    let lat: Double
    let lon: Double
    let sessions: Int
    let energyKwh: Double
    let radiusM: Double

    var id: String { "\(label)|\(lat)|\(lon)" }

    /// The web's words: a public AC place is "Away".
    var title: String {
        switch label {
        case "Home": return "Home"
        case "Public": return "Away"
        case "Fast": return "Fast"
        default: return "Other"
        }
    }

    /// The place colours the Money split and the Activity rail use.
    var key: MoneyLogic.SplitKey {
        switch label {
        case "Home": return .home
        case "Public": return .away
        case "Fast": return .dcfc
        default: return .unknown
        }
    }

    /// The circle's radius in points: 6 for a single visit, growing with the square root of the sessions, at most 18.
    var circleRadius: Double { min(18, 6 + 2 * (Double(max(1, sessions)) - 1).squareRoot()) }

    /// Places with a usable position, the busiest first.
    static func shown(_ places: [ChargePlace]) -> [ChargePlace] {
        places.filter { !($0.lat == 0 && $0.lon == 0) && abs($0.lat) <= 90 && abs($0.lon) <= 180 && $0.sessions > 0 }
            .sorted { $0.sessions != $1.sessions ? $0.sessions > $1.sessions : $0.energyKwh > $1.energyKwh }
    }
}

/// The places on our own basemap: a circle per place, sized by sessions, in its place colour. A picture, like the
/// route map: no user location, no pan or zoom.
struct ChargePlacesMapView: UIViewRepresentable {
    let styleJSON: String
    let places: [ChargePlace]

    static let sourceID = "rivolt-places"

    func makeCoordinator() -> Coordinator { Coordinator(places: places) }

    func makeUIView(context: Context) -> MLNMapView {
        let map = MLNMapView.displayOnly(styleJSON: styleJSON)
        map.delegate = context.coordinator
        return map
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.places = places
        if map.styleJSON != styleJSON { map.styleJSON = styleJSON }
    }

    final class Coordinator: NSObject, MLNMapViewDelegate {
        var places: [ChargePlace]

        init(places: [ChargePlace]) { self.places = places }

        func mapView(_ map: MLNMapView, didFinishLoading style: MLNStyle) {
            guard !places.isEmpty else { return }
            let features: [MLNPointFeature] = places.map { p in
                let f = MLNPointFeature()
                f.coordinate = CLLocationCoordinate2D(latitude: p.lat, longitude: p.lon)
                f.attributes = ["label": p.label, "radius": p.circleRadius]
                return f
            }
            let source = MLNShapeSource(identifier: ChargePlacesMapView.sourceID, features: features, options: nil)
            style.addSource(source)
            // One layer per label with its colour, the busiest drawn on top of the quiet ones.
            for (label, key) in [("", MoneyLogic.SplitKey.unknown), ("Public", .away), ("Home", .home), ("Fast", .dcfc)] {
                let layer = MLNCircleStyleLayer(identifier: "\(ChargePlacesMapView.sourceID)-\(label.isEmpty ? "other" : label)", source: source)
                layer.predicate = NSPredicate(format: "label == %@", label)
                layer.circleColor = NSExpression(forConstantValue: UIColor(ChargePlacesMapView.color(key)))
                layer.circleOpacity = NSExpression(forConstantValue: 0.85)
                layer.circleRadius = NSExpression(forKeyPath: "radius")
                layer.circleStrokeColor = NSExpression(forConstantValue: UIColor(Theme.background))
                layer.circleStrokeWidth = NSExpression(forConstantValue: 1.5)
                style.addLayer(layer)
            }
            var coords = places.map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) }
            if coords.count == 1 {
                map.setCenter(coords[0], zoomLevel: 12, animated: false)
            } else {
                map.setVisibleCoordinates(&coords, count: UInt(coords.count), edgePadding: UIEdgeInsets(top: 28, left: 28, bottom: 28, right: 28), animated: false)
            }
        }
    }

    /// The dot colours of the Money split and the Activity rail.
    static func color(_ key: MoneyLogic.SplitKey) -> Color {
        switch key {
        case .home, .ac: return Theme.home
        case .away, .publicAC: return Theme.away
        case .dcfc: return Theme.fast
        case .unknown: return Theme.textTertiary
        }
    }
}
