import MapLibre
import SwiftUI

/// One drive's route on the map, as the web draws it: the recorded trace snapped to roads through our own Valhalla,
/// coloured by speed (mph buckets) over a faint glow, routed gaps solid and unknown ones dashed, with start and end
/// pins and a speed legend, over our basemap. The recorded line shows at once and is replaced when the snapped one
/// arrives; without samples it's the stored route, without that the ends. The drive and its samples come from the
/// screen (DriveDetailModel, shared with the timeline), held in memory while it is open. No location services.
struct DriveRouteMap: View {
    let phase: DriveDetailModel.Phase
    /// The timeline's cursor: a dot where the truck was at that moment, and its readout in a chip (the web's map cursor).
    var cursor: Date?
    var cursorReadout: String?

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
    /// What the line is coloured by (the web's legend switch); the first the drive supports until a person picks.
    @State private var picked: RouteGradient.Metric?

    private var metric: RouteGradient.Metric {
        guard let drawing else { return .speed }
        let available = drawing.availableMetrics
        return picked.flatMap { available.contains($0) ? $0 : nil } ?? available.first ?? .speed
    }

    var body: some View {
        ZStack {
            if empty {
                MapMessage(text: noLocation ? DriveRouteMap.noLocationText : "No route was recorded for this drive.")
            } else if let drawing, !failed, health != .failed, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                // A picture of the route, not a map to explore: it never takes the detail screen's scrolling.
                RouteMapView(styleJSON: style, drawing: drawing, onHealth: { health = $0 }, onDrawn: { routeDrawn = true }, onPins: { pins = $0 }, metric: metric, cursor: cursorCoord)
                    .allowsHitTesting(false)
                    .accessibilityElement()
                    .accessibilityLabel("Map of the drive route")
                    .routeDecor(drawing: drawing, ready: routeDrawn && health == .ok, pins: pins, times: pillTimes, metric: metric, selection: Binding(get: { metric }, set: { picked = $0 }))
                    .overlay(alignment: .topLeading) {
                        if let cursorReadout, cursorCoord != nil {
                            // The timeline below says it aloud; this is the sighted reader's.
                            Text(cursorReadout).mapCaption().foregroundStyle(Theme.textPrimary).lineLimit(1).fixedSize()
                                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                                .padding(.horizontal, 6).padding(.vertical, 3)
                                .background(Theme.background.opacity(0.85), in: RoundedRectangle(cornerRadius: 6))
                                .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Theme.border, lineWidth: 1))
                                .padding(6)
                                .accessibilityHidden(true)
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
        .opensFullScreenMap(enabled: openable, title: "Drive route", recenterLabel: "Recenter on the route") { recenter in
            if let drawing, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                DriveRouteFullMap(styleJSON: style, drawing: drawing, times: pillTimes, recenter: recenter, initial: metric)
            }
        }
        .task(id: phase) { await load() }
    }

    /// Where the dot goes: the samples' fix at the cursor's moment.
    private var cursorCoord: RouteSnap.Coord? {
        guard let cursor, case .loaded(_, let samples?) = phase else { return nil }
        return RouteSnap.cursorCoord(at: cursor, samples: samples)
    }

    /// The inline map is drawn and the basemap is up: it can open full screen.
    private var openable: Bool { drawing != nil && routeDrawn && health == .ok && !empty && !failed }

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

/// The legend, the credit and the Start/End pills over a route map (inline or full screen). `ready`: the route's layers are on a working basemap.
private struct RouteDecor: ViewModifier {
    let drawing: RouteSnap.Drawing
    let ready: Bool
    let pins: RouteMapView.Pins
    let times: (start: String?, end: String?)?
    var clipPins = false
    var metric = RouteGradient.Metric.speed
    var metrics: [RouteGradient.Metric] = []
    var selection: Binding<RouteGradient.Metric>?

    func body(content: Content) -> some View {
        content
            .overlay(alignment: .bottomTrailing) { MapAttribution() }
            .overlay(alignment: .bottomLeading) {
                if ready, let r = drawing.range(for: metric), metric != .speed || drawing.usesGradient {
                    GradientLegend(lo: r.lo, hi: r.hi, metric: metric, metrics: metrics, selection: selection).padding(4)
                } else if ready, drawing.pieces.contains(where: { $0.speed != nil }) {
                    SpeedLegend(bands: drawing.usedSpeeds).padding(4)
                }
            }
            .overlay {
                if ready, pins.size.width > 0, let times {
                    // A pin panned out of the map takes its pill with it.
                    let bounds = CGRect(origin: .zero, size: pins.size)
                    PinPillsOverlay(size: pins.size, start: pins.start.flatMap { clipPins && !bounds.contains($0) ? nil : $0 },
                                    end: pins.end.flatMap { clipPins && !bounds.contains($0) ? nil : $0 },
                                    startTime: times.start, endTime: times.end,
                                    legend: drawing.pieces.contains(where: { $0.speed != nil }), legendHeight: metrics.count > 1 ? 76 : 52, startLine: pins.startLine, endLine: pins.endLine)
                }
            }
    }
}

extension View {
    fileprivate func routeDecor(drawing: RouteSnap.Drawing, ready: Bool, pins: RouteMapView.Pins, times: (start: String?, end: String?)?, clipPins: Bool = false,
                                metric: RouteGradient.Metric = .speed, selection: Binding<RouteGradient.Metric>? = nil) -> some View {
        modifier(RouteDecor(drawing: drawing, ready: ready, pins: pins, times: times, clipPins: clipPins, metric: metric,
                            metrics: drawing.availableMetrics, selection: selection))
    }
}

/// The drive's route full screen: the same drawing, gradient, pins, pills and legend, to pinch, pan and double-tap zoom.
struct DriveRouteFullMap: View {
    let styleJSON: String
    let drawing: RouteSnap.Drawing
    let times: (start: String?, end: String?)?
    let recenter: Int
    /// The colour mode the inline map was showing.
    var initial = RouteGradient.Metric.speed
    @State private var picked: RouteGradient.Metric?
    @State private var health = MapHealth.ok
    @State private var routeDrawn = false
    @State private var pins = RouteMapView.Pins()

    private var metric: RouteGradient.Metric {
        let available = drawing.availableMetrics
        let want = picked ?? initial
        return available.contains(want) ? want : (available.first ?? .speed)
    }

    var body: some View {
        RouteMapView(styleJSON: styleJSON, drawing: drawing, onHealth: { health = $0 }, onDrawn: { routeDrawn = true }, onPins: { pins = $0 },
                     interactive: true, recenter: recenter, metric: metric)
            .accessibilityElement()
            .accessibilityLabel("Map of the drive route, full screen")
            .accessibilityHint("Pinch to zoom, drag to move")
            .routeDecor(drawing: drawing, ready: routeDrawn && health == .ok, pins: pins, times: times, clipPins: true, metric: metric,
                        selection: Binding(get: { metric }, set: { picked = $0 }))
            .overlay { if health == .noTiles { MapUnavailableOverlay() } }
    }
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

/// The legend of the smooth gradient: the title, a bar in the ramp's colours, and the two ends of the drive's own scale ("32 mph" and "55 mph",
/// in the account's units), as the web's legend card.
struct GradientLegend: View {
    let lo: Double
    let hi: Double
    var metric: RouteGradient.Metric = .speed
    /// The colour modes the drive supports, and the one shown: the switch appears when there is more than one.
    var metrics: [RouteGradient.Metric] = []
    var selection: Binding<RouteGradient.Metric>?
    @Environment(\.colorScheme) private var scheme

    static let title = "Speed"
    static func title(_ m: RouteGradient.Metric) -> String {
        switch m {
        case .speed: return "Speed"
        case .efficiency: return "Efficiency (est.)"
        case .elevation: return "Elevation"
        }
    }

    /// What the colours mean, said in full for VoiceOver (efficiency is worked out from the battery, not Rivian's own figure).
    static func spoken(_ m: RouteGradient.Metric) -> String {
        switch m {
        case .speed: return "speed"
        case .efficiency: return "efficiency, estimated from the battery's change"
        case .elevation: return "elevation"
        }
    }

    private var unit: RouteGradient.DistanceUnit { DisplayUnits.distanceMetric ? .kilometres : .miles }
    var labels: (min: String, max: String) { RouteGradient.legend(lo: lo, hi: hi, metric: metric, unit: unit) }

    /// "Speed 12–64 mph": the two ends share the unit.
    var line: String {
        let l = labels
        guard let space = l.max.firstIndex(of: " ") else { return "\(Self.title(metric)) \(l.min)–\(l.max)" }
        let unitText = String(l.max[l.max.index(after: space)...])
        let minNumber = l.min.split(separator: " ").first.map(String.init) ?? l.min
        return "\(Self.title(metric)) \(minNumber)–\(l.max[..<space]) \(unitText)"
    }

    var body: some View {
        let traits = UITraitCollection(userInterfaceStyle: scheme == .dark ? .dark : .light)
        // One line, "Speed 12–64 mph", over a bar as wide as the line; a switch above it when the drive has more than one colour mode.
        VStack(alignment: .leading, spacing: 3) {
            if metrics.count > 1, let selection {
                MetricSwitch(metrics: metrics, selection: selection)
            }
            Text(line).lineLimit(1).fixedSize()
            RoundedRectangle(cornerRadius: 1.5)
                .fill(LinearGradient(gradient: RouteRamp.ramp(metric).gradient(traits: traits), startPoint: .leading, endPoint: .trailing))
                .frame(height: 3)
        }
        .mapCaption()
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .foregroundStyle(Theme.textBody)
        .fixedSize()
        .padding(.horizontal, 6).padding(.vertical, 4)
        .background(Theme.background.opacity(0.78), in: RoundedRectangle(cornerRadius: 6))
        .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Theme.border, lineWidth: 1))
        .accessibilityElement(children: metrics.count > 1 ? .contain : .ignore)
        .accessibilityLabel("Route coloured by \(Self.spoken(metric)), from \(labels.min) to \(labels.max)")
    }
}

/// "Speed | Efficiency | Elevation": the route's colour modes, as the web's legend switch.
struct MetricSwitch: View {
    let metrics: [RouteGradient.Metric]
    @Binding var selection: RouteGradient.Metric

    static func label(_ m: RouteGradient.Metric) -> String {
        switch m {
        case .speed: return "Speed"
        case .efficiency: return "Efficiency"
        case .elevation: return "Elevation"
        }
    }

    var body: some View {
        HStack(spacing: 2) {
            ForEach(metrics, id: \.self) { m in
                Button { selection = m } label: {
                    Text(Self.label(m)).lineLimit(1).fixedSize()
                        .fontWeight(.medium)
                        .padding(.horizontal, 6).padding(.vertical, 3)
                        .foregroundStyle(m == selection ? Theme.textPrimary : Theme.textSecondary)
                        .background(m == selection ? Theme.borderStrong : Color.clear, in: RoundedRectangle(cornerRadius: 5))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Colour by \(Self.label(m).lowercased())")
                .accessibilityAddTraits(m == selection ? .isSelected : [])
            }
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
    /// A map to explore (pinch, pan, double-tap zoom; never rotate or tilt), as on the full-screen screen.
    var interactive = false
    /// Bump to put the camera back on the whole route (the full-screen map's recenter button).
    var recenter = 0
    /// What the gradient is coloured by.
    var metric: RouteGradient.Metric = .speed
    /// Where the timeline's cursor is: a dot over the route (nil: none).
    var cursor: RouteSnap.Coord?
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
        let map = MLNMapView.displayOnly(styleJSON: styleJSON, watch: context.coordinator, interactive: interactive)
        if interactive, onPins != nil { context.coordinator.onCamera = { [weak c = context.coordinator] m in c?.cameraMoved(m) } }
        if onStop != nil {
            map.addGestureRecognizer(UITapGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.tapped(_:))))
        }
        return map
    }

    func updateUIView(_ map: MLNMapView, context: Context) {
        context.coordinator.onStop = onStop
        context.coordinator.setStyle(styleJSON, on: map)
        context.coordinator.setCursor(cursor)
        context.coordinator.setMetric(metric, on: map)
        context.coordinator.update(drawing, on: map)
        context.coordinator.recenter(recenter, on: map)
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
            // (On the gradient the glow is a wider, fainter copy of the gradient itself, added with it.)
            if !gradientOn { style.addLayer(lineLayer("glow", "pieces", color: Theme.routeGlow, width: 9, opacity: 0.18)) }
            style.addLayer(lineLayer("routed", "routed", color: Theme.routeGap, width: 2.5, opacity: 0.75))
            let dashed = lineLayer("gaps", "gaps", color: Theme.routeGap, width: 2, opacity: 0.7)
            dashed.lineDashPattern = NSExpression(forConstantValue: [3, 3])
            dashed.lineCap = NSExpression(forConstantValue: "butt")
            style.addLayer(dashed)
            if gradientOn {
                addGradientRuns(drawing, map: map, style: style)
            } else {
                for b in [nil] + RouteSnap.Speed.allCases.map(Optional.some) {
                    let l = lineLayer("speed\(b?.rawValue ?? -1)", "pieces", color: RouteMapView.color(b), width: 3, opacity: 0.95)
                    l.predicate = NSPredicate(format: "b == %d", b?.rawValue ?? -1)
                    style.addLayer(l)
                }
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
            // The timeline's cursor dot, over everything but nothing it covers matters (6 pt, white ring).
            let cursorSource = MLNShapeSource(identifier: "\(RouteMapView.sourceID)-cursor", shape: nil, options: nil)
            style.addSource(cursorSource)
            let dot = MLNCircleStyleLayer(identifier: "\(RouteMapView.sourceID)-cursor", source: cursorSource)
            dot.circleRadius = NSExpression(forConstantValue: 6)
            dot.circleColor = NSExpression(forConstantValue: UIColor(Theme.textPrimary))
            dot.circleStrokeColor = NSExpression(forConstantValue: UIColor(Theme.background))
            dot.circleStrokeWidth = NSExpression(forConstantValue: 2)
            style.addLayer(dot)
            self.cursorSource = cursorSource
            setCursor(cursorAt)
            if !keepCamera { fit(map) }
            DispatchQueue.main.async { self.onDrawn() }
        }

        /// The line as a smooth gradient of speed: each unbroken run is its own source (the gradient is along one line) and layer, coloured by the
        /// speed at each vertex on the web's scale (5th to 95th percentile), under the pins.
        /// The line is a gradient: of speed when it varies enough, and of another mode once that is picked.
        private var gradientOn: Bool { metric == .speed ? drawing.usesGradient : (drawing.range(for: metric) != nil && !drawing.runs.isEmpty) }

        private func addGradientRuns(_ d: RouteSnap.Drawing, map: MLNMapView, style: MLNStyle) {
            guard let range = d.range(for: metric) else { return }
            let traits = map.traitCollection
            let below = style.layer(withIdentifier: "\(RouteMapView.sourceID)-pin-start")
            for (i, run) in d.runs.enumerated() {
                var coords = run.coords.map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) }
                let line = MLNPolylineFeature(coordinates: &coords, count: UInt(coords.count))
                let source = MLNShapeSource(identifier: "\(RouteMapView.sourceID)-run-\(i)", shape: line, options: [.lineDistanceMetrics: true])
                style.addSource(source)
                let layer = MLNLineStyleLayer(identifier: "\(RouteMapView.sourceID)-run-\(i)", source: source)
                let stops = RouteGradient.stops(progress: run.progress, values: run.filledValues(metric), lo: range.lo, hi: range.hi)
                var colours: [NSNumber: UIColor] = [:]
                for s in stops { colours[NSNumber(value: s.offset)] = RouteRamp.ramp(metric).color(at: s.t, traits: traits) }
                let gradient = NSExpression(forMLNInterpolating: NSExpression.lineProgressVariable, curveType: .linear, parameters: nil, stops: NSExpression(forConstantValue: colours))
                func style_(_ l: MLNLineStyleLayer, width: Double, opacity: Double) {
                    l.lineGradient = gradient
                    l.lineWidth = NSExpression(forConstantValue: width)
                    l.lineOpacity = NSExpression(forConstantValue: opacity)
                    l.lineCap = NSExpression(forConstantValue: "round")
                    l.lineJoin = NSExpression(forConstantValue: "round")
                }
                let look = RouteSnap.GradientStyle.of(run)
                var layers: [MLNLineStyleLayer] = []
                // The glow: the same gradient, wider and fainter, under the line (not under a stretch the router guessed).
                if let g = look.glow {
                    let glow = MLNLineStyleLayer(identifier: "\(RouteMapView.sourceID)-run-glow-\(i)", source: source)
                    style_(glow, width: g.width, opacity: g.opacity)
                    layers.append(glow)
                }
                style_(layer, width: look.width, opacity: look.opacity)
                layers.append(layer)
                for l in layers { if let below { style.insertLayer(l, below: below) } else { style.addLayer(l) } }
            }
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
            let runsChanged = d.runs != drawing.runs || d.usesGradient != drawing.usesGradient
            drawing = d
            guard !sources.isEmpty else { return }
            for (name, shape) in Self.shapes(d) { sources[name]?.shape = shape }
            // The snapped line replaced the recorded one: its runs (and so its gradient layers) are different ones. Start the map's layers over.
            if runsChanged, let style = map.style { styleLoaded(map, style) }
            // Picking a stop only moves the ring: the camera stays where the person left it.
            if refit { fit(map) }
        }

        override func resized(_ map: MLNMapView) { fit(map) }

        private var cursorSource: MLNShapeSource?
        private var cursorAt: RouteSnap.Coord?

        /// The dot for the timeline's cursor, moved in place (no layer rebuild, the camera stays).
        func setCursor(_ c: RouteSnap.Coord?) {
            cursorAt = c
            cursorSource?.shape = c.map { c in
                let f = MLNPointFeature()
                f.coordinate = CLLocationCoordinate2D(latitude: c.lat, longitude: c.lon)
                return f
            }
        }

        private var metric = RouteGradient.Metric.speed
        private var keepCamera = false
        /// A colour mode was picked: the gradient layers start over in the new colours, the camera stays where it is.
        func setMetric(_ m: RouteGradient.Metric, on map: MLNMapView) {
            guard m != metric else { return }
            metric = m
            guard !sources.isEmpty, let style = map.style else { return }
            keepCamera = true
            styleLoaded(map, style)
            keepCamera = false
        }

        private var recentered = 0
        /// The recenter button was pressed: the whole route in view again, eased there.
        func recenter(_ count: Int, on map: MLNMapView) {
            guard count != recentered else { return }
            recentered = count
            fitCamera(map, animated: true)
        }

        /// A pan or zoom: the pins' places moved, and the pills with them.
        func cameraMoved(_ map: MLNMapView) { reportPins(map) }

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

        private func fitCamera(_ map: MLNMapView, animated: Bool = false) {
            guard map.bounds.width > 0, map.bounds.height > 0 else { return }
            var coords = (drawing.allCoords + [drawing.start, drawing.end].compactMap { $0 })
                .map { CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon) }
            guard !coords.isEmpty else { return }
            let lats = coords.map(\.latitude), lons = coords.map(\.longitude)
            // One spot (a frozen fix): centred, as the web does, never a zero-size fit.
            if (lats.max()! - lats.min()!) < 1e-4 && (lons.max()! - lons.min()!) < 1e-4 {
                map.setCenter(coords[0], zoomLevel: 15, animated: animated)
                return
            }
            map.setVisibleCoordinates(&coords, count: UInt(coords.count), edgePadding: UIEdgeInsets(top: 24, left: 24, bottom: 32, right: 24), animated: animated)
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

    /// The camera moved (a person panned or zoomed a full-screen map).
    var onCamera: ((MLNMapView) -> Void)?
    func mapViewRegionIsChanging(_ map: MLNMapView) { onCamera?(map) }
    func mapView(_ map: MLNMapView, regionDidChangeAnimated animated: Bool) { onCamera?(map) }

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
