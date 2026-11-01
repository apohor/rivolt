import MapLibre
import SwiftUI

/// One drive's route on the map, as the web draws it: the recorded trace snapped to roads through our own Valhalla,
/// coloured by speed (mph buckets) over a faint glow, routed gaps solid and unknown ones dashed, with start and end
/// pins and a speed legend, over our basemap. The recorded line shows at once and is replaced when the snapped one
/// arrives; without samples it's the stored route, without that the ends. The drive and its samples come from the
/// screen (DriveDetailModel, shared with the timeline), held in memory while it is open. No location services.
struct DriveRouteMap: View {
    let phase: DriveDetailModel.Phase

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    @State private var drawing: RouteSnap.Drawing?
    @State private var failed = false
    @State private var empty = false
    /// The nothing-to-draw drive was one whose truck sent no usable live location (the server says so).
    @State private var noLocation = false
    /// How the basemap is doing once the drive's route arrived.
    @State private var health = MapHealth.ok
    /// The route's layers are on the map (the speed legend shows only over a drawn speed route).
    @State private var routeDrawn = false
    /// The pins' positions in the map once it has fitted the route (for the Start and End pills), with the map's size.
    @State private var pins = RouteMapView.Pins()

    var body: some View {
        ZStack {
            if empty {
                MapMessage(text: noLocation ? DriveRouteMap.noLocationText : "No route was recorded for this drive.")
            } else if let drawing, !failed, health != .failed, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                // A picture of the route, not a map to explore: it never takes the detail screen's scrolling.
                RouteMapView(styleJSON: style, drawing: drawing, onHealth: { health = $0 }, onDrawn: { routeDrawn = true }, onPins: { pins = $0 })
                    .allowsHitTesting(false)
                    .accessibilityElement()
                    .accessibilityLabel("Map of the drive route")
                    .overlay(alignment: .bottomTrailing) { MapAttribution() }
                    .overlay(alignment: .bottomLeading) {
                        if routeDrawn, health == .ok, drawing.pieces.contains(where: { $0.speed != nil }) { SpeedLegend(bands: drawing.usedSpeeds).padding(4) }
                    }
                    .overlay {
                        if routeDrawn, health == .ok, pins.size.width > 0, let times = pillTimes {
                            PinPillsOverlay(size: pins.size, start: pins.start, end: pins.end, startTime: times.start, endTime: times.end,
                                            legend: drawing.pieces.contains(where: { $0.speed != nil }), startLine: pins.startLine, endLine: pins.endLine)
                        }
                    }
                    .overlay { if health == .noTiles { MapUnavailableOverlay() } }
            } else if failed || health == .failed || drawing != nil {
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
        .task(id: phase) { await load() }
    }

    /// "9:15 PM" and "11:28 PM": the drive's own start and end, in the phone's time zone and clock style (the web's pills do the same).
    private var pillTimes: (start: String?, end: String?)? {
        guard case .loaded(let d, _) = phase else { return nil }
        func f(_ date: Date?) -> String? { date?.formatted(date: .omitted, time: .shortened) }
        return (f(d.startedAt), f(d.endedAt))
    }

    private func load() async {
        MapNetwork.prepare(server: state.mapServer, token: state.mapToken)
        health = .ok
        routeDrawn = false
        let client = state.client
        let drive: DriveRoute, samples: [DriveSample]?
        switch phase {
        case .loading:
            return
        case .failed:
            failed = true
            return
        case .loaded(let d, let s):
            drive = d
            samples = s
            failed = false
        }
        // The drive's samples (and 10 minutes either side, for the parked pins), as the web's detail page.
        if let samples, let path = RouteSnap.path(drive: drive, samples: samples) {
            drawing = RouteSnap.raw(path)
            if let snapped = await RouteSnap.snap(path, source: client), !Task.isCancelled { drawing = snapped }
            return
        }
        // No samples: the stored route (no speeds), else nothing to draw.
        let stored = drive.points
        if stored.count >= 2 {
            drawing = RouteSnap.Drawing(pieces: [RouteSnap.Piece(coords: stored, speed: nil)], routedGaps: [], gaps: [],
                                        start: stored.first, end: stored.last, hasSpeed: false)
        } else {
            noLocation = drive.locationUnavailable == true
            empty = true
        }
    }

    /// What the server observed, with no cause and no fault.
    static let noLocationText = "The vehicle didn't report its location during this drive."
}

/// The speed legend as a small corner card, laid out like the web's: a title line that carries the unit ("Speed, mph"), then a bar of the
/// bands this drive uses with each band's lower bound under its own segment. A column is as wide as its label, so ticks never collide or
/// wrap whatever the text size (it follows Dynamic Type up to a point: past that the map card would run out of room).
struct SpeedLegend: View {
    let bands: [RouteSnap.Speed]
    static let title = "Speed, mph"

    var body: some View {
        if !bands.isEmpty {
            VStack(alignment: .leading, spacing: 2) {
                Text(Self.title).foregroundStyle(Theme.textTertiary).lineLimit(1).fixedSize()
                // Six points between columns and a 24 pt minimum: "50" and "65+" stay apart at the largest size this legend follows.
                HStack(spacing: 6) {
                    ForEach(bands, id: \.rawValue) { b in
                        VStack(spacing: 1) {
                            RoundedRectangle(cornerRadius: 1.5).fill(RouteMapView.color(b)).frame(height: 3)
                            Text(b.shortLabel).lineLimit(1).fixedSize()
                        }
                        .frame(minWidth: 24)
                    }
                }
            }
            .mapCaption()
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .foregroundStyle(Theme.textBody)
            // Its own width (the bars are flexible shapes and would otherwise take the whole map).
            .fixedSize()
            .padding(.horizontal, 6).padding(.vertical, 4)
            .background(Theme.background.opacity(0.78), in: RoundedRectangle(cornerRadius: 6))
            .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Theme.border, lineWidth: 1))
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Route coloured by speed in miles per hour: \(bands.map(\.label).joined(separator: ", "))")
        }
    }
}

/// MapLibre's view of a drawing: a glow, the line in speed buckets, routed and unknown gaps, and the pins. The
/// sources are updated in place when the snapped drawing replaces the recorded one.
struct RouteMapView: UIViewRepresentable {
    let styleJSON: String
    let drawing: RouteSnap.Drawing
    var onHealth: (MapHealth) -> Void = { _ in }
    /// The route's layers went onto the map.
    var onDrawn: () -> Void = {}
    /// The pins' positions in the map (and its size) each time the camera is fitted: where the Start and End pills go. Unused by the plan map.
    var onPins: ((Pins) -> Void)?

    struct Pins: Equatable {
        var size: CGSize = .zero
        var start: CGPoint?
        var end: CGPoint?
        /// The route's first stretch from each pin, in map points (about 100 pt of it): a pill never lies along it.
        var startLine: [CGPoint] = []
        var endLine: [CGPoint] = []
    }
    /// A tap on a charging stop's pin (its index in `drawing.stops`).
    var onStop: ((Int) -> Void)?

    static let sourceID = "rivolt-route"

    /// A speed bucket's colour (nil: the plain line).
    static func color(_ b: RouteSnap.Speed?) -> Color {
        switch b {
        case .under5?: return Theme.routeSpeedUnder5
        case .to25?: return Theme.routeSpeed5to25
        case .to50?: return Theme.routeSpeed25to50
        case .to65?: return Theme.routeSpeed50to65
        case .over65?: return Theme.routeSpeed65Plus
        case nil: return Theme.routeLine
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(drawing: drawing, onHealth: onHealth, onDrawn: onDrawn, onPins: onPins) }

    func makeUIView(context: Context) -> MLNMapView {
        let map = MLNMapView.displayOnly(styleJSON: styleJSON, watch: context.coordinator)
        if onStop != nil {
            map.addGestureRecognizer(UITapGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.tapped(_:))))
        }
        return map
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.onStop = onStop
        context.coordinator.setStyle(styleJSON, on: map)
        context.coordinator.update(drawing, on: map)
    }

    final class Coordinator: MapLoadWatch {
        private var drawing: RouteSnap.Drawing
        private var sources: [String: MLNShapeSource] = [:]
        private let onDrawn: () -> Void
        private let onPins: ((Pins) -> Void)?
        var onStop: ((Int) -> Void)?

        init(drawing: RouteSnap.Drawing, onHealth: @escaping (MapHealth) -> Void, onDrawn: @escaping () -> Void = {}, onPins: ((Pins) -> Void)? = nil) {
            self.drawing = drawing
            self.onDrawn = onDrawn
            self.onPins = onPins
            super.init(onHealth: onHealth)
        }

        private static func line(_ coords: [RouteSnap.Coord], attributes: [String: Any] = [:]) -> MLNPolylineFeature {
            var c = coords.map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) }
            let f = MLNPolylineFeature(coordinates: &c, count: UInt(c.count))
            f.attributes = attributes
            return f
        }

        /// The features of each source for a drawing.
        private static func shapes(_ d: RouteSnap.Drawing) -> [String: MLNShape] {
            let pieces = d.pieces.map { line($0.coords, attributes: ["b": $0.speed?.rawValue ?? -1]) }
            let pins: [MLNPointFeature] = [(d.start, "start"), (d.end, "end")].compactMap { c, kind in
                guard let c else { return nil }
                let f = MLNPointFeature()
                f.coordinate = CLLocationCoordinate2D(latitude: c.lat, longitude: c.lon)
                f.attributes = ["kind": kind]
                return f
            }
            let stops: [MLNPointFeature] = d.stops.enumerated().map { i, s in
                let f = MLNPointFeature()
                f.coordinate = CLLocationCoordinate2D(latitude: s.coord.lat, longitude: s.coord.lon)
                f.attributes = ["i": i, "label": s.label]
                return f
            }
            let along: [MLNPointFeature] = d.along.enumerated().map { i, s in
                let f = MLNPointFeature()
                f.coordinate = CLLocationCoordinate2D(latitude: s.coord.lat, longitude: s.coord.lon)
                f.attributes = ["i": i, "label": s.label]
                return f
            }
            // The picked pin again on its own, so it can be drawn over the rest: a stop, or past the stops a charger along the route.
            let focus: [MLNPointFeature] = d.focus.flatMap { i -> MLNPointFeature? in
                if d.stops.indices.contains(i) { return stops[i] }
                let j = i - d.stops.count
                return along.indices.contains(j) ? along[j] : nil
            }.map { [$0] } ?? []
            return [
                "stops": MLNShapeCollectionFeature(shapes: stops),
                "along": MLNShapeCollectionFeature(shapes: along),
                "focus": MLNShapeCollectionFeature(shapes: focus.map { f in
                    let copy = MLNPointFeature()
                    copy.coordinate = f.coordinate
                    return copy
                }),
                "pieces": MLNShapeCollectionFeature(shapes: pieces),
                "routed": MLNShapeCollectionFeature(shapes: d.routedGaps.map { line($0) }),
                "gaps": MLNShapeCollectionFeature(shapes: d.gaps.map { line([$0.from, $0.to]) }),
                "pins": MLNShapeCollectionFeature(shapes: pins),
            ]
        }

        override func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {
            MapLoadWatch.clear(style, prefix: RouteMapView.sourceID)
            sources = [:]
            for (name, shape) in Self.shapes(drawing) {
                let s = MLNShapeSource(identifier: "\(RouteMapView.sourceID)-\(name)", shape: shape, options: nil)
                style.addSource(s)
                sources[name] = s
            }
            func lineLayer(_ id: String, _ source: String, color: Color, width: Double, opacity: Double) -> MLNLineStyleLayer {
                let l = MLNLineStyleLayer(identifier: "\(RouteMapView.sourceID)-\(id)", source: sources[source]!)
                l.lineColor = NSExpression(forConstantValue: UIColor(color))
                l.lineWidth = NSExpression(forConstantValue: width)
                l.lineOpacity = NSExpression(forConstantValue: opacity)
                l.lineCap = NSExpression(forConstantValue: "round")
                l.lineJoin = NSExpression(forConstantValue: "round")
                return l
            }
            // The glow under everything recorded, then the gaps, then the line by speed, then the pins.
            style.addLayer(lineLayer("glow", "pieces", color: Theme.routeGlow, width: 9, opacity: 0.18))
            style.addLayer(lineLayer("routed", "routed", color: Theme.routeGap, width: 2.5, opacity: 0.75))
            let dashed = lineLayer("gaps", "gaps", color: Theme.routeGap, width: 2, opacity: 0.7)
            dashed.lineDashPattern = NSExpression(forConstantValue: [3, 3])
            dashed.lineCap = NSExpression(forConstantValue: "butt")
            style.addLayer(dashed)
            for b in [nil] + RouteSnap.Speed.allCases.map(Optional.some) {
                let l = lineLayer("speed\(b?.rawValue ?? -1)", "pieces", color: RouteMapView.color(b), width: 3, opacity: 0.95)
                l.predicate = NSPredicate(format: "b == %d", b?.rawValue ?? -1)
                style.addLayer(l)
            }
            for (kind, color) in [("start", Theme.routeStart), ("end", Theme.routeEnd)] {
                let c = MLNCircleStyleLayer(identifier: "\(RouteMapView.sourceID)-pin-\(kind)", source: sources["pins"]!)
                c.predicate = NSPredicate(format: "kind == %@", kind)
                c.circleColor = NSExpression(forConstantValue: UIColor(color))
                c.circleRadius = NSExpression(forConstantValue: 6)
                c.circleStrokeColor = NSExpression(forConstantValue: UIColor.white)
                c.circleStrokeWidth = NSExpression(forConstantValue: 2)
                style.addLayer(c)
            }
            do {
                let small = MLNCircleStyleLayer(identifier: "\(RouteMapView.sourceID)-along", source: sources["along"]!)
                small.circleColor = NSExpression(forConstantValue: UIColor(Theme.chargePin))
                small.circleOpacity = NSExpression(forConstantValue: 0.75)
                small.circleRadius = NSExpression(forConstantValue: 5)
                small.circleStrokeColor = NSExpression(forConstantValue: UIColor(Theme.chargePinInk))
                small.circleStrokeWidth = NSExpression(forConstantValue: 1.5)
                style.addLayer(small)
            }
            do {
                let dot = MLNCircleStyleLayer(identifier: "\(RouteMapView.sourceID)-stop", source: sources["stops"]!)
                dot.circleColor = NSExpression(forConstantValue: UIColor(Theme.chargePin))
                dot.circleRadius = NSExpression(forConstantValue: 8)
                dot.circleStrokeColor = NSExpression(forConstantValue: UIColor(Theme.chargePinInk))
                dot.circleStrokeWidth = NSExpression(forConstantValue: 2)
                style.addLayer(dot)
                let text = MLNSymbolStyleLayer(identifier: "\(RouteMapView.sourceID)-stop-label", source: sources["stops"]!)
                text.text = NSExpression(forKeyPath: "label")
                text.textFontNames = NSExpression(forConstantValue: ["Noto Sans Medium"])
                text.textFontSize = NSExpression(forConstantValue: ThemeTokens.typeMapCaption)
                text.textColor = NSExpression(forConstantValue: UIColor(Theme.textPrimary))
                text.textHaloColor = NSExpression(forConstantValue: UIColor(Theme.background))
                text.textHaloWidth = NSExpression(forConstantValue: 1.5)
                text.textAnchor = NSExpression(forConstantValue: "bottom")
                text.textOffset = NSExpression(forConstantValue: NSValue(cgVector: CGVector(dx: 0, dy: -1.1)))
                style.addLayer(text)
                let ring = MLNCircleStyleLayer(identifier: "\(RouteMapView.sourceID)-stop-focus", source: sources["focus"]!)
                ring.circleColor = NSExpression(forConstantValue: UIColor(Theme.accent))
                ring.circleRadius = NSExpression(forConstantValue: 12)
                ring.circleStrokeColor = NSExpression(forConstantValue: UIColor.white)
                ring.circleStrokeWidth = NSExpression(forConstantValue: 3)
                style.addLayer(ring)
            }
            fit(map)
            DispatchQueue.main.async { self.onDrawn() }
        }

        /// The stop under a tap, if any (a generous 22 pt box, for a thumb).
        @objc func tapped(_ g: UITapGestureRecognizer) {
            guard let map = g.view as? MLNMapView else { return }
            let p = g.location(in: map)
            let box = CGRect(x: p.x - 22, y: p.y - 22, width: 44, height: 44)
            // A stop pin wins over a charger under it; a charger is numbered after the stops.
            if let i = map.visibleFeatures(in: box, styleLayerIdentifiers: ["\(RouteMapView.sourceID)-stop"]).first?.attribute(forKey: "i") as? Int {
                onStop?(i)
            } else if let j = map.visibleFeatures(in: box, styleLayerIdentifiers: ["\(RouteMapView.sourceID)-along"]).first?.attribute(forKey: "i") as? Int {
                onStop?(drawing.stops.count + j)
            }
        }

        /// The snapped drawing replaces the recorded one in the same sources.
        func update(_ d: RouteSnap.Drawing, on map: MLNMapView) {
            guard d != drawing else { return }
            var sameButFocus = drawing
            sameButFocus.focus = d.focus
            let refit = sameButFocus != d
            drawing = d
            guard !sources.isEmpty else { return }
            for (name, shape) in Self.shapes(d) { sources[name]?.shape = shape }
            // Picking a stop only moves the ring: the camera stays where the person left it.
            if refit { fit(map) }
        }

        override func resized(_ map: MLNMapView) { fit(map) }

        /// The camera on the whole drawing. MapLibre ignores a fit while the view has no size, so it's made again
        /// when the view gets one (`resized`).
        private func fit(_ map: MLNMapView) {
            fitCamera(map)
            reportPins(map)
        }

        /// Where the two pins are on screen, for the pills (the map is a picture, so this is the final camera).
        private func reportPins(_ map: MLNMapView) {
            guard let onPins, map.bounds.width > 0 else { return }
            func point(_ c: RouteSnap.Coord?) -> CGPoint? { c.map { map.convert(CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon), toPointTo: map) } }
            // About 100 pt of the line from each pin, followed from the ends of the drawn line.
            func stretch(_ coords: [RouteSnap.Coord]) -> [CGPoint] {
                var out: [CGPoint] = []
                for c in coords {
                    guard let p = point(c) else { continue }
                    if let last = out.last, hypot(p.x - last.x, p.y - last.y) < 4 { continue }
                    out.append(p)
                    if let first = out.first, hypot(p.x - first.x, p.y - first.y) >= 100 { break }
                }
                return out
            }
            let first = drawing.pieces.first?.coords ?? []
            let last = drawing.pieces.last?.coords.reversed() ?? []
            let pins = Pins(size: map.bounds.size, start: point(drawing.start), end: point(drawing.end),
                            startLine: stretch(first), endLine: stretch(Array(last)))
            DispatchQueue.main.async { onPins(pins) }
        }

        private func fitCamera(_ map: MLNMapView) {
            guard map.bounds.width > 0, map.bounds.height > 0 else { return }
            var coords = (drawing.allCoords + [drawing.start, drawing.end].compactMap { $0 })
                .map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) }
            guard !coords.isEmpty else { return }
            let lats = coords.map(\.latitude), lons = coords.map(\.longitude)
            // One spot (a frozen fix): centred, as the web does, never a zero-size fit.
            if (lats.max()! - lats.min()!) < 1e-4 && (lons.max()! - lons.min()!) < 1e-4 {
                map.setCenter(coords[0], zoomLevel: 15, animated: false)
                return
            }
            map.setVisibleCoordinates(&coords, count: UInt(coords.count), edgePadding: UIEdgeInsets(top: 24, left: 24, bottom: 32, right: 24), animated: false)
        }
    }
}

/// The tiles are OpenStreetMap data (ODbL): a small credit in the corner of every map, as the web's. The Protomaps
/// style and its licence are credited in Acknowledgements (owner's call, Oct 22).
struct MapAttribution: View {
    static let text = "© OpenStreetMap"

    var body: some View {
        Text(Self.text)
            .mapCaption()
            .foregroundStyle(Theme.textSecondary)
            .padding(.horizontal, 3).padding(.vertical, 1)
            .background(Theme.background.opacity(0.6), in: RoundedRectangle(cornerRadius: 3))
            .padding(4)
            .accessibilityLabel("Map data OpenStreetMap")
    }
}

extension View {
    /// The one size of text over a map (`type.mapCaption`): the speed chip, the credit, the legends and the pin labels all read the same.
    func mapCaption() -> some View {
        font(Theme.sans(ThemeTokens.typeMapCaption, .regular, relativeTo: .caption2))
    }
}

extension MLNMapView {
    /// A map that is a picture: our style (as JSON, never fetched), no user location, no pan, zoom, rotation or tilt,
    /// no compass, the credit drawn by the SwiftUI side (MapAttribution) instead of the info button, and no MapLibre
    /// mark (its BSD licence asks for the notice in Acknowledgements, not a logo). `watch` is its delegate from the
    /// start, so it sees the style even when MapLibre loads it inside the initialiser. `interactive` allows pan and
    /// pinch-zoom (never rotation or tilt), for a map that is a screen's main content (the Activity map).
    static func displayOnly(styleJSON: String, watch: MapLoadWatch, interactive: Bool = false) -> DisplayMapView {
        let map = DisplayMapView(frame: .zero, styleJSON: styleJSON)
        map.showsUserLocation = false
        map.isRotateEnabled = false
        map.isPitchEnabled = false
        map.isScrollEnabled = interactive
        map.isZoomEnabled = interactive
        map.compassView.isHidden = true
        map.attributionButton.isHidden = true
        map.logoView.isHidden = true
        watch.attach(map, styleJSON: styleJSON)
        return map
    }
}

/// A MapLibre view that says when its size changes: SwiftUI creates it at zero size and lays it out later, and a
/// camera fit made before that is ignored.
final class DisplayMapView: MLNMapView {
    var onResize: ((MLNMapView) -> Void)?
    private var laidOutSize = CGSize.zero

    override func layoutSubviews() {
        super.layoutSubviews()
        guard bounds.width > 0, bounds.height > 0, bounds.size != laidOutSize else { return }
        laidOutSize = bounds.size
        onResize?(self)
    }
}

/// "Map unavailable." over a map whose tiles haven't loaded (it goes when they do).
struct MapUnavailableOverlay: View {
    var body: some View {
        MapMessage(text: "Map unavailable.")
            .padding(.horizontal, 8).padding(.vertical, 4)
            .background(Theme.card.opacity(0.9), in: RoundedRectangle(cornerRadius: Theme.radiusInner))
    }
}

/// A quiet line in a map's place when there is nothing to draw.
struct MapMessage: View {
    let text: String
    var body: some View { Text(text).themeText(.subline, color: Theme.textTertiary) }
}

/// How a map's basemap is doing, for the line over it.
enum MapHealth: Equatable {
    case ok
    /// Every tile request so far failed: shown over the map, and cleared when a tile arrives (MapLibre retries, so a
    /// slow connection recovers on its own).
    case noTiles
    /// The map itself failed (its style or the connection): for good.
    case failed
}

/// The basemap's health from what MapLibre reports, kept apart from the delegate so it can be tested. Each event
/// returns the new health when it changed.
struct MapLoadState {
    private(set) var health = MapHealth.ok
    private var tilesLoaded = 0
    private var tileErrors = 0

    /// A tile arrived (network or cache): a "no tiles yet" line clears.
    mutating func tileLoaded() -> MapHealth? {
        tilesLoaded += 1
        return health == .noTiles ? set(.ok) : nil
    }

    mutating func tileFailed() { tileErrors += 1 }

    /// The map settled: every tile so far failed and none loaded.
    mutating func idle() -> MapHealth? {
        tileErrors > 0 && tilesLoaded == 0 ? set(.noTiles) : nil
    }

    /// The map itself failed (style or connection): for good.
    mutating func mapFailed() -> MapHealth? { set(.failed) }

    private mutating func set(_ h: MapHealth) -> MapHealth? {
        guard health != .failed, h != health else { return nil }
        health = h
        return h
    }
}

/// What both maps' MapLibre delegates share: the style's arrival (`styleLoaded`), the view's size (`resized`), and
/// how the basemap is doing.
class MapLoadWatch: NSObject, MLNMapViewDelegate {
    private let onHealth: (MapHealth) -> Void
    private var state = MapLoadState()
    /// The style JSON last given to the map (compared here, not with MapLibre's `styleJSON`, which needn't echo it).
    private var styleJSON: String?

    init(onHealth: @escaping (MapHealth) -> Void) { self.onHealth = onHealth }

    /// Adds this map's sources and layers to a style that finished loading. Called again for a new style (light or
    /// dark), and possibly twice for one, so it starts by clearing its own (`clear`).
    func styleLoaded(_ map: MLNMapView, _ style: MLNStyle) {}

    /// The view got or changed its size.
    func resized(_ map: MLNMapView) {}

    /// Becomes `map`'s delegate. A style given as JSON can finish loading inside MapLibre's initialiser, before
    /// there was a delegate to tell, and nothing was ever drawn on it (0.2.6: no route, no pins, the camera on the
    /// whole world): a style already in is handled here.
    func attach(_ map: DisplayMapView, styleJSON: String) {
        self.styleJSON = styleJSON
        map.delegate = self
        map.onResize = { [weak self] in self?.resized($0) }
        if let style = map.style { styleLoaded(map, style) }
    }

    /// Switches the map to `json` when it differs from the last one given (light/dark); true when it did.
    @discardableResult
    func setStyle(_ json: String, on map: MLNMapView) -> Bool {
        guard json != styleJSON else { return false }
        styleJSON = json
        map.styleJSON = json
        return true
    }

    /// Removes the sources and layers whose identifiers start with `prefix`.
    static func clear(_ style: MLNStyle, prefix: String) {
        for layer in style.layers where layer.identifier.hasPrefix(prefix) { style.removeLayer(layer) }
        for source in style.sources where source.identifier.hasPrefix(prefix) { style.removeSource(source) }
    }

    func mapView(_ map: MLNMapView, didFinishLoading style: MLNStyle) { styleLoaded(map, style) }

    func mapViewDidFailLoadingMap(_ map: MLNMapView, withError error: Error) { report(state.mapFailed()) }

    func mapView(_ map: MLNMapView, tileDidTriggerAction operation: MLNTileOperation, x: Int, y: Int, z: Int, wrap: Int,
                 overscaledZ: Int, sourceID: String) {
        switch operation {
        case .loadFromNetwork, .loadFromCache: report(state.tileLoaded())
        case .error: state.tileFailed()
        default: break
        }
    }

    func mapViewDidBecomeIdle(_ map: MLNMapView) { report(state.idle()) }

    private func report(_ h: MapHealth?) {
        guard let h else { return }
        DispatchQueue.main.async { self.onHealth(h) }
    }
}
