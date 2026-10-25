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
/// on the phone (as the web does), at most `maxRows` of each (as /api/activity), each route simplified, and held in
/// memory only. A new month or truck drops the old rows before anything else; sign-out and a switch to another server
/// drop them too (AppState).
@MainActor
@Observable
final class ActivityMapModel {
    /// A drive as the map draws it: its simplified line.
    struct Drive: Equatable, Identifiable {
        let id: String
        let startedAt: Date
        let points: [DriveRoute.Point]
    }

    /// /api/activity's cap per kind.
    static let maxRows = 2000
    /// Douglas-Peucker tolerance in degrees (about 5 m): a month of long routes stays light, and nothing the eye sees
    /// at a city zoom changes.
    static let tolerance = 0.00005

    private(set) var drives: [Drive] = []
    private(set) var charges: [MapCharge] = []
    private(set) var isLoading = false
    private(set) var failed = false
    /// The truck and month on screen.
    private(set) var key: String?

    func load(source: some ActivityMapSource, vehicleID: String, month: PeriodMonth, force: Bool = false, calendar: Calendar = .current) async {
        let k = "\(vehicleID)|\(month.start.timeIntervalSince1970)"
        if k == key && !failed && !force { return }
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
            drives = allDrives
                .filter { $0.startedAt >= since && $0.startedAt < until }
                .sorted { $0.startedAt > $1.startedAt }
                .prefix(Self.maxRows)
                .compactMap { d in
                    let pts = Self.simplify(d.points, tolerance: Self.tolerance)
                    return pts.count >= 2 ? Drive(id: d.id, startedAt: d.startedAt, points: pts) : nil
                }
            charges = Array(allCharges
                .filter { $0.startedAt >= since && $0.startedAt < until && $0.hasFix }
                .sorted { $0.startedAt > $1.startedAt }
                .prefix(Self.maxRows))
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

    /// Douglas-Peucker on lat/lon: the ends kept, and every point farther than `tolerance` from the simplified line.
    static func simplify(_ pts: [DriveRoute.Point], tolerance: Double) -> [DriveRoute.Point] {
        guard pts.count > 2 else { return pts }
        var keep = [Bool](repeating: false, count: pts.count)
        keep[0] = true
        keep[pts.count - 1] = true
        var stack = [(0, pts.count - 1)]
        while let (a, b) = stack.popLast() {
            guard b > a + 1 else { continue }
            let p = pts[a], q = pts[b]
            let dx = q.lon - p.lon, dy = q.lat - p.lat
            let len2 = dx * dx + dy * dy
            var far = 0.0, at = a
            for i in (a + 1)..<b {
                let r = pts[i]
                var t = len2 > 0 ? ((r.lon - p.lon) * dx + (r.lat - p.lat) * dy) / len2 : 0
                t = min(1, max(0, t))
                let ex = p.lon + t * dx - r.lon, ey = p.lat + t * dy - r.lat
                let d = (ex * ex + ey * ey).squareRoot()
                if d > far { far = d; at = i }
            }
            if far > tolerance {
                keep[at] = true
                stack.append((a, at))
                stack.append((at, b))
            }
        }
        return pts.indices.filter { keep[$0] }.map { pts[$0] }
    }
}

/// What a tap on the Activity map picked: a drive (by id, with its start for a round trip's later legs) or a charge.
enum ActivityMapPick: Equatable {
    case drive(id: String, startedAt: Date)
    case charge(id: String)
}

/// The month's drives (their routes) and charges (shaped by power: a bolt for fast, a ring for L2, a small dot for L1,
/// in the web's colours) on our own map, as the web's Activity map: pan and pinch-zoom, a tap opens the drive or
/// charge. The filter picks the layers.
struct ActivityMapView: View {
    let drives: [ActivityMapModel.Drive]
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
            if health != .failed, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                ActivityMapRepresentable(styleJSON: style, drives: drives, charges: charges, filter: filter,
                                         onHealth: { health = $0 }, onPick: onPick)
                    .accessibilityElement()
                    .accessibilityLabel("Map of this month's drives and charges")
                    // The legend above the credit, each at most two lines.
                    .overlay(alignment: .bottom) {
                        VStack(alignment: .leading, spacing: Theme.unit) {
                            ActivityMapLegend(drives: showsDrives, charges: showsCharges ? Set(charges.map(\.bucket)) : [])
                            HStack {
                                Spacer(minLength: 0)
                                MapAttribution()
                            }
                        }
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
        .onAppear { MapNetwork.prepare(server: state.mapServer, token: state.mapToken) }
    }
}

/// A charge's mark by power, the same on the map and in the legend: never colour alone.
struct ChargeBucketMark: View {
    let bucket: MapCharge.Bucket

    var body: some View {
        let d = ThemeTokens.mapDot
        switch bucket {
        case .fast:
            ZStack {
                Circle().fill(ActivityMapRepresentable.color(.fast))
                Image(systemName: "bolt.fill").font(.system(size: d * 0.6, weight: .bold)).foregroundStyle(Theme.chargePinInk)
            }
            .frame(width: ActivityMapRepresentable.fastBadge, height: ActivityMapRepresentable.fastBadge)
        case .l2:
            Circle().strokeBorder(ActivityMapRepresentable.color(.l2), lineWidth: ActivityMapRepresentable.ringWidth).frame(width: d, height: d)
        case .l1:
            Circle().fill(ActivityMapRepresentable.color(.l1)).frame(width: d / 2, height: d / 2).frame(width: d, height: d)
        }
    }
}

/// What the marks mean: the drives' line and the charge buckets on the map. Wraps at large text, two lines at most.
struct ActivityMapLegend: View {
    let drives: Bool
    let charges: Set<MapCharge.Bucket>

    var body: some View {
        if drives || !charges.isEmpty {
            ThemeFlow(spacing: 2 * Theme.unit, rowSpacing: Theme.unit) {
                if drives {
                    HStack(spacing: Theme.unit) {
                        Capsule().fill(Theme.mapDrive).frame(width: 3 * Theme.unit, height: 2 * ThemeTokens.mapRouteWidth)
                        Text("Drives").lineLimit(1)
                    }
                }
                ForEach(MapCharge.Bucket.allCases.filter { charges.contains($0) }, id: \.rawValue) { b in
                    HStack(spacing: Theme.unit) {
                        ChargeBucketMark(bucket: b)
                        Text(b.label).lineLimit(1)
                    }
                }
            }
            .mapCaption()
            .foregroundStyle(Theme.textBody)
            .padding(.horizontal, 1.5 * Theme.unit).padding(.vertical, 0.5 * Theme.unit)
            .background(Theme.background.opacity(0.78), in: RoundedRectangle(cornerRadius: Theme.radiusInner))
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityElement(children: .combine)
        }
    }
}

/// MapLibre's view of the month: every drive's line with its start and end dots, and every charge's mark. Pan and
/// zoom are on; the camera fits what the filter shows when the data or the filter changes, and once the view first
/// has its size.
struct ActivityMapRepresentable: UIViewRepresentable {
    let styleJSON: String
    let drives: [ActivityMapModel.Drive]
    let charges: [MapCharge]
    let filter: ActivityFilter
    var onHealth: (MapHealth) -> Void = { _ in }
    var onPick: (ActivityMapPick) -> Void = { _ in }

    static let sourceID = "rivolt-activity"
    /// The web's fitBounds maxZoom for its overview maps.
    static let maxFitZoom = 14.0
    /// A fast charge's bolt badge (a dot and a ring wide).
    static let fastBadge = CGFloat(ThemeTokens.mapDot + 2 * ThemeTokens.mapDotRing) + Theme.unit
    /// An L2 charge's ring.
    static let ringWidth = CGFloat(1.5 * ThemeTokens.mapDotRing)
    /// The finger's reach for a tap: Apple's 44 pt minimum hit area.
    static let tapTarget = 11 * Theme.unit
    static let fastImage = "rivolt-activity-fast"

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
        private(set) var drives: [ActivityMapModel.Drive]
        private(set) var charges: [MapCharge]
        private(set) var filter: ActivityFilter
        var onPick: (ActivityMapPick) -> Void
        private var sources: [String: MLNShapeSource] = [:]
        private var fitted = false

        init(drives: [ActivityMapModel.Drive], charges: [MapCharge], filter: ActivityFilter, onHealth: @escaping (MapHealth) -> Void,
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
                [(d.points.first, "start"), (d.points.last, "end")].compactMap { p, end in
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

        static var driveLayers: [String] { [layerID("drives"), layerID("end-start"), layerID("end-end")] }
        static var chargeLayers: [String] { MapCharge.Bucket.allCases.map { layerID("charge-\($0.rawValue)") } }

        override func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {
            MapLoadWatch.clear(style, prefix: ActivityMapRepresentable.sourceID)
            style.setImage(Self.fastBadge(), forName: ActivityMapRepresentable.fastImage)
            sources = [:]
            for (name, shape) in shapes() {
                let s = MLNShapeSource(identifier: Self.layerID(name), shape: shape, options: nil)
                style.addSource(s)
                sources[name] = s
            }
            let line = MLNLineStyleLayer(identifier: Self.layerID("drives"), source: sources["drives"]!)
            line.lineColor = NSExpression(forConstantValue: UIColor(Theme.mapDrive))
            line.lineWidth = NSExpression(forConstantValue: ThemeTokens.mapRouteWidth)
            line.lineOpacity = NSExpression(forConstantValue: ThemeTokens.mapRouteOpacity)
            line.lineCap = NSExpression(forConstantValue: "round")
            line.lineJoin = NSExpression(forConstantValue: "round")
            style.addLayer(line)
            func circle(_ id: String, _ source: String, _ predicate: NSPredicate) -> MLNCircleStyleLayer {
                let c = MLNCircleStyleLayer(identifier: Self.layerID(id), source: sources[source]!)
                c.predicate = predicate
                c.circleRadius = NSExpression(forConstantValue: ThemeTokens.mapDot / 2)
                c.circleStrokeColor = NSExpression(forConstantValue: UIColor(Theme.chargePinInk))
                c.circleStrokeWidth = NSExpression(forConstantValue: ThemeTokens.mapDotRing)
                return c
            }
            for (end, color) in [("start", Theme.routeStart), ("end", Theme.routeEnd)] {
                let c = circle("end-\(end)", "ends", NSPredicate(format: "end == %@", end))
                c.circleColor = NSExpression(forConstantValue: UIColor(color))
                style.addLayer(c)
            }
            // L1: a small dot. L2: a ring. Fast: the bolt badge.
            let l1 = circle("charge-\(MapCharge.Bucket.l1.rawValue)", "charges", NSPredicate(format: "bucket == %d", MapCharge.Bucket.l1.rawValue))
            l1.circleColor = NSExpression(forConstantValue: UIColor(Theme.chargeL1))
            l1.circleRadius = NSExpression(forConstantValue: ThemeTokens.mapDot / 4)
            l1.circleStrokeWidth = NSExpression(forConstantValue: 0)
            style.addLayer(l1)
            let l2 = circle("charge-\(MapCharge.Bucket.l2.rawValue)", "charges", NSPredicate(format: "bucket == %d", MapCharge.Bucket.l2.rawValue))
            l2.circleColor = NSExpression(forConstantValue: UIColor(Theme.background))
            l2.circleStrokeColor = NSExpression(forConstantValue: UIColor(Theme.chargeL2))
            l2.circleStrokeWidth = NSExpression(forConstantValue: ActivityMapRepresentable.ringWidth)
            style.addLayer(l2)
            let fast = MLNSymbolStyleLayer(identifier: Self.layerID("charge-\(MapCharge.Bucket.fast.rawValue)"), source: sources["charges"]!)
            fast.predicate = NSPredicate(format: "bucket == %d", MapCharge.Bucket.fast.rawValue)
            fast.iconImageName = NSExpression(forConstantValue: ActivityMapRepresentable.fastImage)
            fast.iconAllowsOverlap = NSExpression(forConstantValue: true)
            style.addLayer(fast)
            applyFilter(style)
            fit(map)
        }

        func update(drives: [ActivityMapModel.Drive], charges: [MapCharge], filter: ActivityFilter, on map: MLNMapView) {
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

        /// A tap opens what's under the finger (a 44 pt reach): a charge first, then a drive's ends, then its line.
        @objc func tapped(_ gesture: UITapGestureRecognizer) {
            guard let map = gesture.view as? MLNMapView else { return }
            let p = gesture.location(in: map)
            let r = ActivityMapRepresentable.tapTarget / 2
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

        /// The fast charge's badge: a filled circle in the fast colour, a dark ring and a bolt.
        static func fastBadge() -> UIImage {
            let side = ActivityMapRepresentable.fastBadge
            let ring = ThemeTokens.mapDotRing
            return UIGraphicsImageRenderer(size: CGSize(width: side, height: side)).image { _ in
                let path = UIBezierPath(ovalIn: CGRect(x: ring / 2, y: ring / 2, width: side - ring, height: side - ring))
                UIColor(Theme.chargeFast).setFill()
                path.fill()
                UIColor(Theme.chargePinInk).setStroke()
                path.lineWidth = ring
                path.stroke()
                let config = UIImage.SymbolConfiguration(pointSize: ThemeTokens.mapDot * 0.6, weight: .bold)
                if let bolt = UIImage(systemName: "bolt.fill", withConfiguration: config)?
                    .withTintColor(UIColor(Theme.chargePinInk), renderingMode: .alwaysOriginal) {
                    bolt.draw(at: CGPoint(x: (side - bolt.size.width) / 2, y: (side - bolt.size.height) / 2))
                }
            }
        }
    }
}
