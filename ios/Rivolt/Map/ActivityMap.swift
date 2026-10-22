import MapLibre
import Observation
import SwiftUI

/// The two lists behind the Activity map, kept apart from `APIClient` so tests can fake them.
protocol ActivityMapSource: Sendable {
    func mapDrives(vehicleID: String) async throws -> [MapDrive]
    func mapCharges(vehicleID: String) async throws -> [MapCharge]
}

extension APIClient: ActivityMapSource {}

/// The Activity map's drives and charges for one truck and month: read when the Map view opens, filtered to the month
/// on the phone (as the web does), and held in memory only. A new month or truck drops the old rows before anything
/// else; sign-out and a switch to another server drop them too (AppState).
@MainActor
@Observable
final class ActivityMapModel {
    private(set) var drives: [MapDrive] = []
    private(set) var charges: [MapCharge] = []
    private(set) var isLoading = false
    private(set) var failed = false
    /// The truck and month on screen.
    private(set) var key: String?

    func load(source: some ActivityMapSource, vehicleID: String, month: PeriodMonth, calendar: Calendar = .current) async {
        let k = "\(vehicleID)|\(month.start.timeIntervalSince1970)"
        if k == key && !failed { return }
        if k != key { clear() }
        key = k
        isLoading = true
        defer { isLoading = false }
        let (since, until) = month.range(calendar: calendar)
        do {
            async let ds = source.mapDrives(vehicleID: vehicleID)
            async let cs = source.mapCharges(vehicleID: vehicleID)
            let (allDrives, allCharges) = try await (ds, cs)
            guard key == k else { return }
            drives = allDrives.filter { $0.startedAt >= since && $0.startedAt < until && !$0.points.isEmpty }
            charges = allCharges.filter { $0.startedAt >= since && $0.startedAt < until && $0.hasFix }
            failed = false
        } catch {
            guard key == k else { return }
            failed = true
        }
    }

    /// Forgets every row (sign-out, another server, another truck or month).
    func clear() {
        drives = []
        charges = []
        key = nil
        failed = false
    }
}

/// What a tap on the Activity map picked: a drive (by id, with its start for a round trip's later legs) or a charge.
enum ActivityMapPick: Equatable {
    case drive(id: String, startedAt: Date)
    case charge(id: String)
}

/// The month's drives (their routes) and charges (dots in the web's power colours) on our own map, as the web's
/// Activity map: pan and pinch-zoom, a tap opens the drive or charge. The filter picks the layers.
struct ActivityMapView: View {
    let drives: [MapDrive]
    let charges: [MapCharge]
    let filter: ActivityFilter
    var onPick: (ActivityMapPick) -> Void = { _ in }

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    @State private var health = MapHealth.ok

    private var showsDrives: Bool { filter != .charges && !drives.isEmpty }
    private var showsCharges: Bool { filter != .drives && !charges.isEmpty }

    var body: some View {
        ZStack {
            if health != .failed, let style = MapStyle.json(dark: scheme == .dark, server: state.serverURL) {
                ActivityMapRepresentable(styleJSON: style, drives: drives, charges: charges, filter: filter,
                                         onHealth: { health = $0 }, onPick: onPick)
                    .accessibilityElement()
                    .accessibilityLabel("Map of the month's drives and charges")
                    .overlay(alignment: .bottomTrailing) { MapAttribution() }
                    .overlay(alignment: .bottomLeading) {
                        ActivityMapLegend(drives: showsDrives, charges: showsCharges ? Set(charges.map(\.bucket)) : [])
                            .padding(Theme.unit)
                    }
                    .overlay { if health == .noTiles { MapUnavailableOverlay() } }
            } else {
                MapMessage(text: "Map unavailable.")
            }
        }
        .background(Theme.card)
        .clipShape(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .onAppear { MapNetwork.prepare(server: state.serverURL, token: try? state.tokens.token(for: state.serverURL)) }
    }
}

/// What the colours mean: the drives' line and the charge buckets on the map.
struct ActivityMapLegend: View {
    let drives: Bool
    let charges: Set<MapCharge.Bucket>

    var body: some View {
        if drives || !charges.isEmpty {
            HStack(spacing: 2 * Theme.unit) {
                if drives {
                    HStack(spacing: Theme.unit) {
                        Capsule().fill(Theme.activityRoute).frame(width: 3 * Theme.unit, height: ThemeTokens.mapRouteWidth * 2)
                        Text("Drives")
                    }
                }
                ForEach(MapCharge.Bucket.allCases.filter { charges.contains($0) }, id: \.rawValue) { b in
                    HStack(spacing: Theme.unit) {
                        Circle().fill(ActivityMapRepresentable.color(b)).frame(width: ThemeTokens.mapDot, height: ThemeTokens.mapDot)
                        Text(b.label)
                    }
                }
            }
            .font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2))
            .foregroundStyle(Theme.textBody)
            .padding(.horizontal, 1.5 * Theme.unit).padding(.vertical, 0.5 * Theme.unit)
            .background(Theme.background.opacity(0.78), in: RoundedRectangle(cornerRadius: Theme.radiusInner))
            .accessibilityElement(children: .combine)
        }
    }
}

/// MapLibre's view of the month: every drive's line with its start and end dots, and every charge's dot. Pan and
/// zoom are on; the camera fits what the filter shows when the data or the filter changes, and once the view first
/// has its size.
struct ActivityMapRepresentable: UIViewRepresentable {
    let styleJSON: String
    let drives: [MapDrive]
    let charges: [MapCharge]
    let filter: ActivityFilter
    var onHealth: (MapHealth) -> Void = { _ in }
    var onPick: (ActivityMapPick) -> Void = { _ in }

    static let sourceID = "rivolt-activity"
    /// The web's fitBounds maxZoom for its overview maps.
    static let maxFitZoom = 14.0

    static func color(_ b: MapCharge.Bucket) -> Color {
        switch b {
        case .fast: return Theme.chargeFast
        case .l2: return Theme.chargeL2
        case .l1: return Theme.chargeL1
        }
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(drives: drives, charges: charges, filter: filter, onHealth: onHealth, onPick: onPick)
    }

    func makeUIView(context: Context) -> MLNMapView {
        let map = MLNMapView.displayOnly(styleJSON: styleJSON, watch: context.coordinator, interactive: true)
        let tap = UITapGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.tapped(_:)))
        for r in map.gestureRecognizers ?? [] where (r as? UITapGestureRecognizer)?.numberOfTapsRequired == 2 {
            tap.require(toFail: r)
        }
        map.addGestureRecognizer(tap)
        return map
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.onPick = onPick
        context.coordinator.setStyle(styleJSON, on: map)
        context.coordinator.update(drives: drives, charges: charges, filter: filter, on: map)
    }

    final class Coordinator: MapLoadWatch {
        private(set) var drives: [MapDrive]
        private(set) var charges: [MapCharge]
        private(set) var filter: ActivityFilter
        var onPick: (ActivityMapPick) -> Void
        private var sources: [String: MLNShapeSource] = [:]
        private var fitted = false

        init(drives: [MapDrive], charges: [MapCharge], filter: ActivityFilter, onHealth: @escaping (MapHealth) -> Void,
             onPick: @escaping (ActivityMapPick) -> Void = { _ in }) {
            self.drives = drives
            self.charges = charges
            self.filter = filter
            self.onPick = onPick
            super.init(onHealth: onHealth)
        }

        static func layerID(_ name: String) -> String { "\(ActivityMapRepresentable.sourceID)-\(name)" }

        private static func coordinate(_ p: DriveRoute.Point) -> CLLocationCoordinate2D {
            CLLocationCoordinate2D(latitude: p.lat, longitude: p.lon)
        }

        private func shapes() -> [String: MLNShape] {
            let lines: [MLNPolylineFeature] = drives.map { d in
                var c = d.points.map(Self.coordinate)
                let f = MLNPolylineFeature(coordinates: &c, count: UInt(c.count))
                f.attributes = ["id": d.id, "at": d.startedAt.timeIntervalSince1970]
                return f
            }
            let ends: [MLNPointFeature] = drives.flatMap { d -> [MLNPointFeature] in
                let pts = d.points
                return [(pts.first, "start"), (pts.last, "end")].compactMap { p, end in
                    guard let p else { return nil }
                    let f = MLNPointFeature()
                    f.coordinate = Self.coordinate(p)
                    f.attributes = ["id": d.id, "at": d.startedAt.timeIntervalSince1970, "end": end]
                    return f
                }
            }
            let dots: [MLNPointFeature] = charges.map { c in
                let f = MLNPointFeature()
                f.coordinate = CLLocationCoordinate2D(latitude: c.lat, longitude: c.lon)
                f.attributes = ["id": c.id, "bucket": c.bucket.rawValue]
                return f
            }
            return [
                "drives": MLNShapeCollectionFeature(shapes: lines),
                "ends": MLNShapeCollectionFeature(shapes: ends),
                "charges": MLNShapeCollectionFeature(shapes: dots),
            ]
        }

        private static var driveLayers: [String] { [layerID("drives"), layerID("end-start"), layerID("end-end")] }
        private static var chargeLayers: [String] { MapCharge.Bucket.allCases.map { layerID("charge-\($0.rawValue)") } }

        override func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {
            MapLoadWatch.clear(style, prefix: ActivityMapRepresentable.sourceID)
            sources = [:]
            for (name, shape) in shapes() {
                let s = MLNShapeSource(identifier: Self.layerID(name), shape: shape, options: nil)
                style.addSource(s)
                sources[name] = s
            }
            let line = MLNLineStyleLayer(identifier: Self.layerID("drives"), source: sources["drives"]!)
            line.lineColor = NSExpression(forConstantValue: UIColor(Theme.activityRoute))
            line.lineWidth = NSExpression(forConstantValue: ThemeTokens.mapRouteWidth)
            line.lineOpacity = NSExpression(forConstantValue: ThemeTokens.mapRouteOpacity)
            line.lineCap = NSExpression(forConstantValue: "round")
            line.lineJoin = NSExpression(forConstantValue: "round")
            style.addLayer(line)
            func dot(_ id: String, _ source: String, _ predicate: NSPredicate, _ color: Color) -> MLNCircleStyleLayer {
                let c = MLNCircleStyleLayer(identifier: Self.layerID(id), source: sources[source]!)
                c.predicate = predicate
                c.circleColor = NSExpression(forConstantValue: UIColor(color))
                c.circleRadius = NSExpression(forConstantValue: ThemeTokens.mapDot / 2)
                c.circleStrokeColor = NSExpression(forConstantValue: UIColor(Theme.chargePinInk))
                c.circleStrokeWidth = NSExpression(forConstantValue: ThemeTokens.mapDotRing)
                return c
            }
            style.addLayer(dot("end-start", "ends", NSPredicate(format: "end == %@", "start"), Theme.routeStart))
            style.addLayer(dot("end-end", "ends", NSPredicate(format: "end == %@", "end"), Theme.routeEnd))
            for b in MapCharge.Bucket.allCases {
                style.addLayer(dot("charge-\(b.rawValue)", "charges", NSPredicate(format: "bucket == %d", b.rawValue), ActivityMapRepresentable.color(b)))
            }
            applyFilter(style)
            fit(map)
        }

        func update(drives: [MapDrive], charges: [MapCharge], filter: ActivityFilter, on map: MLNMapView) {
            let dataChanged = drives != self.drives || charges != self.charges
            guard dataChanged || filter != self.filter else { return }
            self.drives = drives
            self.charges = charges
            self.filter = filter
            if dataChanged { for (name, shape) in shapes() { sources[name]?.shape = shape } }
            if let style = map.style { applyFilter(style) }
            fit(map)
        }

        private func applyFilter(_ style: MLNStyle) {
            for id in Self.driveLayers { style.layer(withIdentifier: id)?.isVisible = filter != .charges }
            for id in Self.chargeLayers { style.layer(withIdentifier: id)?.isVisible = filter != .drives }
        }

        override func resized(_ map: MLNMapView) {
            if !fitted { fit(map) }
        }

        /// Everything the filter shows, as the web's fitBounds (24 pt padding, at most zoom 14).
        private func fit(_ map: MLNMapView) {
            guard map.bounds.width > 0, map.bounds.height > 0 else { return }
            var coords: [CLLocationCoordinate2D] = []
            if filter != .charges { coords += drives.flatMap { $0.points.map(Self.coordinate) } }
            if filter != .drives { coords += charges.map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) } }
            guard !coords.isEmpty else { return }
            fitted = true
            let lats = coords.map(\.latitude), lons = coords.map(\.longitude)
            if (lats.max()! - lats.min()!) < 1e-4 && (lons.max()! - lons.min()!) < 1e-4 {
                map.setCenter(coords[0], zoomLevel: ActivityMapRepresentable.maxFitZoom, animated: false)
                return
            }
            let pad = 6 * Theme.unit
            map.setVisibleCoordinates(&coords, count: UInt(coords.count), edgePadding: UIEdgeInsets(top: pad, left: pad, bottom: pad, right: pad), animated: false)
            if map.zoomLevel > ActivityMapRepresentable.maxFitZoom {
                map.setZoomLevel(ActivityMapRepresentable.maxFitZoom, animated: false)
            }
        }

        /// A tap opens what's under the finger: a charge's dot first, then a drive's ends, then its line.
        @objc func tapped(_ gesture: UITapGestureRecognizer) {
            guard let map = gesture.view as? MLNMapView else { return }
            let p = gesture.location(in: map)
            let r = 3 * Theme.unit
            let rect = CGRect(x: p.x - r, y: p.y - r, width: 2 * r, height: 2 * r)
            for layers in [Self.chargeLayers, [Self.layerID("end-start"), Self.layerID("end-end")], [Self.layerID("drives")]] {
                let visible = Set(layers.filter { map.style?.layer(withIdentifier: $0)?.isVisible == true })
                guard !visible.isEmpty, let f = map.visibleFeatures(in: rect, styleLayerIdentifiers: visible).first,
                      let pick = Self.pick(f.attributes) else { continue }
                onPick(pick)
                return
            }
        }

        /// A feature's attributes as a pick: charges carry a bucket, drives their start.
        static func pick(_ a: [String: Any]) -> ActivityMapPick? {
            guard let id = a["id"] as? String else { return nil }
            if a["bucket"] != nil { return .charge(id: id) }
            guard let at = (a["at"] as? NSNumber)?.doubleValue else { return nil }
            return .drive(id: id, startedAt: Date(timeIntervalSince1970: at))
        }
    }
}
