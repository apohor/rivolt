import SwiftUI

/// The route of a plan on the map, kept apart from the view so a test can see what is drawn.
enum PlanRouteLogic {
    /// "to 72% · 25 min" over a charging stop's pin.
    static func stopLabel(_ s: TripWaypoint) -> String {
        var parts: [String] = []
        if let d = s.departureSoC { parts.append("to \(Int(d.rounded()))%") }
        if let c = s.chargeDurationSec, c > 0 { parts.append(NowFormat.duration(c)) }
        return parts.joined(separator: " · ")
    }

    /// The places the route passes, in order: the start, each stop, the destination.
    static func places(_ route: TripRoute) -> [RouteSnap.Coord] {
        route.waypoints.compactMap { w in w.latitude.flatMap { la in w.longitude.map { RouteSnap.Coord(lat: la, lon: $0) } } }
    }

    /// The places with a different set of the person's own stops: the route's two ends and the stops between them, in the order they were given.
    static func places(_ route: TripRoute, vias: [PlanVia]) -> [RouteSnap.Coord] {
        let all = places(route)
        guard let first = all.first, let last = all.last, all.count >= 2 else { return all }
        return [first] + vias.map { RouteSnap.Coord(lat: $0.lat, lon: $0.lon) } + [last]
    }

    /// Valhalla's route through the places: the start and the destination are hard stops, the chargers are passed through so
    /// a charger the roads can't snap to doesn't fail the route (as the web does).
    static func routeRequest(_ places: [RouteSnap.Coord]) -> ValhallaRouteRequest {
        ValhallaRouteRequest(locations: places.enumerated().map { i, c in
            .init(lat: c.lat, lon: c.lon, type: i == 0 || i == places.count - 1 ? "break" : "through")
        })
    }

    /// The drawing for a line: no speeds, the first and last points as the start and end, a pin for every charging stop.
    static func drawing(route: TripRoute, line: [RouteSnap.Coord], vias: [PlanVia] = []) -> RouteSnap.Drawing? {
        guard line.count >= 2 else { return nil }
        // The charging stops, then the person's own stops (numbered after them).
        let stops = route.chargingStops.compactMap { s -> RouteSnap.Drawing.Stop? in
            guard let la = s.latitude, let lo = s.longitude else { return nil }
            return .init(coord: RouteSnap.Coord(lat: la, lon: lo), label: stopLabel(s))
        } + vias.map { RouteSnap.Drawing.Stop(coord: RouteSnap.Coord(lat: $0.lat, lon: $0.lon), label: "Your stop") }
        return RouteSnap.Drawing(pieces: [RouteSnap.Piece(coords: line, speed: nil)], routedGaps: [], gaps: [],
                                 start: line.first, end: line.last, hasSpeed: false, stops: stops)
    }

    /// The line to draw: the route's stored shape, else the roads through the places, else straight legs between them.
    static func line(route: TripRoute, source: some RouteSnapSource) async -> [RouteSnap.Coord] {
        if let shape = route.routePolyline {
            let stored = DriveRoute.decode(shape, precision: 6)
            if stored.count >= 2 { return stored }
        }
        let places = places(route)
        guard places.count >= 2 else { return places }
        if let r = try? await source.valhallaRoute(routeRequest(places)), let out = roadLine(r), out.count > 1 { return out }
        return places
    }

    /// Valhalla's answer as one line: each leg's shape, the shared point between two legs once. Nil when it has no legs.
    static func roadLine(_ r: ValhallaRouteResponse) -> [RouteSnap.Coord]? {
        guard let legs = r.trip?.legs, !legs.isEmpty else { return nil }
        var out: [RouteSnap.Coord] = []
        for (i, leg) in legs.enumerated() {
            var seg = leg.shape.map { DriveRoute.decode($0, precision: 6) } ?? []
            if i > 0, !seg.isEmpty { seg.removeFirst() }
            out += seg
        }
        return out
    }

    /// What both maps (one route, or the days of a trip) lay over the drawing of the plan: the ringed pin, the layer pins and the weather labels.
    static func overlaid(_ base: RouteSnap.Drawing, focus: Int?, along: [AlongCharger], legTags: [RouteSnap.Drawing.LegTag]) -> RouteSnap.Drawing {
        var d = base
        d.focus = focus
        d.along = alongStops(along)
        d.legTags = legTags
        return d
    }

    /// The pins of one day, in the order they are drawn and numbered: its charging stops that have a place, then its night.
    static func dayPins(_ day: TripMultiday.Day) -> (stops: [TripWaypoint], night: Bool) {
        let stops = (day.plan.best?.chargingStops ?? []).filter { $0.latitude != nil && $0.longitude != nil }
        return (stops, day.overnight != nil && day.end?.latitude != nil && day.end?.longitude != nil)
    }

    /// Where each day's pins start in the drawing's numbering, so a row in a day card and a pin on the map point at one another.
    static func pinOffsets(_ days: [TripMultiday.Day]) -> [Int] {
        var offsets: [Int] = [], n = 0
        for day in days {
            offsets.append(n)
            let p = dayPins(day)
            n += p.stops.count + (p.night ? 1 : 0)
        }
        return offsets
    }

    /// How many pins a two-day trip's drawing numbers (the layer pins come after them).
    static func pinCount(days: [TripMultiday.Day]) -> Int {
        guard let last = days.last, let at = pinOffsets(days).last else { return 0 }
        let p = dayPins(last)
        return at + p.stops.count + (p.night ? 1 : 0)
    }

    /// What the map says about the layer pins on it: "12 places along the route", "1 place along the route", nothing for none.
    static func alongPinsSpoken(_ count: Int) -> String {
        count == 0 ? "" : "\(count) \(count == 1 ? "place" : "places") along the route"
    }

    /// The weather on a plan's legs and how to say it: the account's units, and whether the plan used a driver profile ("Weather and your setup").
    struct Weather: Equatable {
        var legs: [TripWeatherLeg]
        var celsius: Bool
        var metric: Bool
        var withSetup: Bool

        init?(_ plan: TripPlan?, celsius: Bool = DisplayUnits.celsius, metric: Bool = DisplayUnits.distanceMetric) {
            self.init(plan?.weatherAdjustment, celsius: celsius, metric: metric)
        }

        /// A day of a trip: its weather sits beside its plan (older saved trips may carry it in the plan).
        init?(day: TripMultiday.Day, celsius: Bool = DisplayUnits.celsius, metric: Bool = DisplayUnits.distanceMetric) {
            self.init(day.weatherAdjustment ?? day.plan.weatherAdjustment, celsius: celsius, metric: metric)
        }

        init?(_ adj: TripWeatherAdjustment?, celsius: Bool, metric: Bool) {
            guard let adj, !adj.legs.isEmpty else { return nil }
            legs = adj.legs
            self.celsius = celsius
            self.metric = metric
            withSetup = (adj.profileMultiplier ?? 1) != 1
        }
    }

    /// Where leg `from` to `to` is half way along the drawn line (by vertex), or the straight middle when there is no line to follow; the vertices nearest the
    /// two stops bound the leg.
    static func legMiddle(line: [RouteSnap.Coord], from: RouteSnap.Coord, to: RouteSnap.Coord) -> RouteSnap.Coord {
        let straight = RouteSnap.Coord(lat: (from.lat + to.lat) / 2, lon: (from.lon + to.lon) / 2)
        guard line.count >= 3 else { return straight }
        func nearest(_ p: RouteSnap.Coord, from start: Int = 0) -> Int {
            func d(_ c: RouteSnap.Coord) -> Double {
                let dl = c.lat - p.lat, dn = (c.lon - p.lon) * cos(p.lat * .pi / 180)
                return dl * dl + dn * dn
            }
            var best = start
            for i in start..<line.count where d(line[i]) < d(line[best]) { best = i }
            return best
        }
        let a = nearest(from), b = nearest(to, from: a)
        return b - a < 2 ? straight : line[(a + b) / 2]
    }

    /// One weather label per leg that has weather to say, at the leg's middle. Leg i runs from waypoint i to i+1 of the route.
    static func legTags(route: TripRoute, weather: Weather?, line: [RouteSnap.Coord]) -> [RouteSnap.Drawing.LegTag] {
        guard let weather else { return [] }
        let places = route.waypoints.compactMap { w -> RouteSnap.Coord? in
            guard let la = w.latitude, let lo = w.longitude else { return nil }
            return RouteSnap.Coord(lat: la, lon: lo)
        }
        // A waypoint without a place would shift the legs: then the labels have nowhere sure to go.
        guard places.count == route.waypoints.count else { return [] }
        var out: [RouteSnap.Drawing.LegTag] = []
        for (i, leg) in weather.legs.enumerated() where i + 1 < places.count {
            let facts = LegWeather.facts(leg, celsius: weather.celsius, metric: weather.metric)
            guard let text = LegWeather.mapLabel(facts) else { continue }
            out.append(.init(coord: legMiddle(line: line, from: places[i], to: places[i + 1]), text: text,
                             title: legTitle(route, leg: i), lines: legLines(leg, facts: facts, weather: weather)))
        }
        return out
    }

    /// Whether the plan on show (one route, or the days of a trip) has weather to draw: then the Weather button is there.
    static func hasWeather(plan: TripPlan?, night: TripPlanModel.NightState) -> Bool {
        if Weather(plan) != nil { return true }
        if case .planned(let trip) = night { return trip.days.contains { Weather(day: $0) != nil } }
        return false
    }

    /// The labels of a trip's days: each day's own legs on its own line (`lines` has one line for each day that has a route).
    static func legTags(days: [TripMultiday.Day], lines: [[RouteSnap.Coord]]) -> [RouteSnap.Drawing.LegTag] {
        var out: [RouteSnap.Drawing.LegTag] = []
        var k = 0
        for day in days {
            guard let route = day.plan.best else { continue }
            defer { k += 1 }
            guard lines.indices.contains(k) else { continue }
            out += legTags(route: route, weather: Weather(day: day), line: lines[k])
        }
        return out
    }

    /// "Seattle to Spokane": the leg's title (the start of a plan from the truck's own place has no name: "Start").
    static func legTitle(_ route: TripRoute, leg i: Int) -> String {
        func name(_ n: Int) -> String {
            let short = route.waypoints.indices.contains(n) ? PlanSheetLogic.shortPlace(route.waypoints[n].name ?? "") : ""
            return short.isEmpty ? (n == 0 ? "Start" : "the stop") : short
        }
        return "\(name(i)) to \(name(i + 1))"
    }

    /// What the leg's label opens with, in the web's order: the tags, what it feels like, the estimate, the climb and the terrain credit.
    static func legLines(_ leg: TripWeatherLeg, facts: LegWeather.Facts?, weather: Weather) -> [String] {
        let climb = LegWeather.climbLine(climbM: leg.climbM, descentM: leg.descentM, metric: weather.metric)
        let tags = LegWeather.tags(facts).joined(separator: " · ")
        return [
            tags.isEmpty ? nil : tags,
            facts?.feels.map { "Feels like \($0)" },
            LegWeather.estimateLine(multiplier: leg.multiplier, withSetup: weather.withSetup),
            climb,
            climb == nil ? nil : LegWeather.terrainCredit,
        ].compactMap { $0 }
    }

    /// The layer pins (chargers, hotels, campgrounds along the route) as the drawing's own: kind 0 a fast charger, 1 a hotel, 2 a campground, 3 a Medium one.
    static func alongStops(_ along: [AlongCharger]) -> [RouteSnap.Drawing.Stop] {
        along.map { .init(coord: .init(lat: $0.lat, lon: $0.lon), label: "", emphasis: $0.preferred, kind: $0.kind == "hotel" ? 1 : $0.kind == "camp" ? 2 : $0.kind == "l2" ? 3 : 0) }
    }

    /// The days map as shown: the drawing with the ringed pin, the layer pins and, for one day, the camera framed on that day's line.
    static func daysShown(_ base: RouteSnap.Drawing, focus: Int?, along: [AlongCharger], lines: [[RouteSnap.Coord]], day: Int?,
                          days: [TripMultiday.Day] = [], weatherOn: Bool = false) -> RouteSnap.Drawing {
        var d = overlaid(base, focus: focus, along: along, legTags: weatherOn ? legTags(days: days, lines: lines) : [])
        d.fitTo = day.flatMap { n in lines.indices.contains(n - 1) ? lines[n - 1] : nil }
        return d
    }

    /// Whether the line the layers follow starts at the trip's own origin: the whole trip and Day 1 do; Day 2 and later start at an overnight stop.
    static func layerLineStartsAtOrigin(day: Int?) -> Bool { LayerSearch.startsAtOrigin(day: day) }

    /// The line the layers are searched along: the picked day's, else every day's.
    static func layerLine(lines: [[RouteSnap.Coord]], day: Int?) -> [RouteSnap.Coord] {
        day.flatMap { n in lines.indices.contains(n - 1) ? lines[n - 1] : nil } ?? lines.flatMap { $0 }
    }

    /// Both days of a two-day trip as one drawing: each day's line, a pin for every charging stop and one for each night.
    static func drawing(days: [TripMultiday.Day], lines: [[RouteSnap.Coord]]) -> RouteSnap.Drawing? {
        let pieces = lines.filter { $0.count >= 2 }.map { RouteSnap.Piece(coords: $0, speed: nil) }
        guard let first = pieces.first?.coords.first, let last = pieces.last?.coords.last else { return nil }
        var stops: [RouteSnap.Drawing.Stop] = []
        for day in days {
            let pins = dayPins(day)
            for s in pins.stops {
                stops.append(.init(coord: RouteSnap.Coord(lat: s.latitude ?? 0, lon: s.longitude ?? 0), label: stopLabel(s)))
            }
            if pins.night, let la = day.end?.latitude, let lo = day.end?.longitude {
                stops.append(.init(coord: RouteSnap.Coord(lat: la, lon: lo), label: nightLabel(day.overnight, place: day.end)))
            }
        }
        return RouteSnap.Drawing(pieces: pieces, routedGaps: [], gaps: [], start: first, end: last, hasSpeed: false, stops: stops)
    }

    /// "Night: Sample Inn" over the pin where a day ends.
    static func nightLabel(_ o: TripMultiday.Overnight?, place: TripMultiday.Place?) -> String {
        let name = o?.name ?? place?.label
        return name.map { "Night: \($0)" } ?? "Night"
    }
}

/// A planned trip on our own map (the tiles are the signed-in server's). `drawing` is nil until there is something to draw. A tap
/// on a charging stop's pin goes to `onStop` when it is given.
struct PlanMapCanvas: View {
    let drawing: RouteSnap.Drawing?
    /// The radar frame under the route, when the Radar button is on.
    var radar: RadarLayer?
    var onStop: ((Int) -> Void)?
    /// A tap on a leg's weather label goes to `onLegTag` when it is given.
    var onLegTag: ((RouteSnap.Drawing.LegTag) -> Void)?
    /// A map to explore: pinch and pan (never rotate or tilt), filling the room it is given (the plan's full-height map), with the camera
    /// fitted above `bottomInset` points that something else (the itinerary sheet) covers.
    var fills = false
    var bottomInset: CGFloat = 0
    /// What the layer chips over the map's top take: the camera fits below it.
    var topInset: CGFloat = 0
    /// Bump to frame the drawing again.
    var reframe = 0
    var showsCredit = true
    /// A long press on the map (only on the full-height map).
    var onLongPress: ((RouteSnap.Coord) -> Void)?
    var onCenter: ((RouteSnap.Coord) -> Void)?

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    @State private var health = MapHealth.ok
    #if DEBUG
    /// The weather labels the map drew, as elements where they are, on a UI-test launch only (the accessibility tree has the labels' text whether drawn or not).
    @State private var drawnLegTags: [RouteMapView.DrawnLabel] = []
    private var reportsDrawnLegTags: Bool { UITestLaunch.directories != nil }
    #endif

    var body: some View {
        ZStack {
            if health == .failed {
                MapMessage(text: "Map unavailable.")
            } else if let drawing, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                RouteMapView(styleJSON: style, drawing: drawing, onHealth: { health = $0 }, interactive: fills, recenter: reframe, radar: radar, bottomInset: bottomInset, topInset: topInset, onStop: onStop, onLegTag: onLegTag.map { cb in { i in if drawing.legTags.indices.contains(i) { cb(drawing.legTags[i]) } } }, onLongPress: onLongPress, onCenter: onCenter, onDrawnLegTags: drawnLegTagsReport)
                    .accessibilityElement()
                    .accessibilityLabel("Map of the planned route with \(drawing.stops.count) \(drawing.stops.count == 1 ? "pin" : "pins")")
                    // The places drawn along the route (the layer chips' pins), for VoiceOver and for a UI test: nothing is said when there are none.
                    .accessibilityValue(PlanRouteLogic.alongPinsSpoken(drawing.along.count))
                    .overlay(alignment: .bottomTrailing) { if showsCredit { MapAttribution() } }
                    .overlay { if health == .noTiles { MapUnavailableOverlay() } }
                    #if DEBUG
                    .overlay(alignment: .topLeading) {
                        ForEach(Array(drawnLegTags.enumerated()), id: \.offset) { _, t in
                            Color.clear.frame(width: 4, height: 4)
                                .accessibilityElement()
                                .accessibilityIdentifier("drawn-weather-label")
                                .accessibilityLabel(t.text)
                                .offset(x: t.point.x - 2, y: t.point.y - 2)
                        }
                    }
                    #endif
            } else {
                ProgressView()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: fills ? .infinity : nil)
        .frame(height: fills ? nil : 240)
        .background(Theme.card)
        .clipShape(RoundedRectangle(cornerRadius: fills ? 0 : Theme.radiusCard, style: .continuous))
        .overlay { if !fills { RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1) } }
    }

    private var drawnLegTagsReport: (([RouteMapView.DrawnLabel]) -> Void)? {
        #if DEBUG
        if reportsDrawnLegTags { return { drawnLegTags = $0 } }
        #endif
        return nil
    }
}

/// A planned trip on our own map: one route, or both days of a two-day trip. One view with two modes, so what the map does for a plan (the ringed pin,
/// the layer pins, the weather labels and their taps, the radar, the line the layers follow) is the same code for both.
///  - `.route`: the line, the start and the destination, and a pin for each charging stop with its charge-to and minutes; the full-height map can also be edited (the person's own
///    stops, the what-if preview's line, a long press to drop a pin, the crosshair to move a stop).
///  - `.days`: each day's line, a pin for every charging stop and one for each night; the camera can frame one day.
/// A tap on a pin goes to that stop in the list. No location services.
struct PlanMap: View {
    struct OneRoute {
        var route: TripRoute
        /// The weather on the plan's legs, drawn as labels while the Weather toggle is on (nil: none).
        var weather: PlanRouteLogic.Weather?
        /// The person's own stops on this route (pins; the route passes through them).
        var vias: [PlanVia] = []
        /// The stops of a re-plan under way: our own roads line through them is drawn at once, until the planner's answer replaces it.
        var pendingVias: [PlanVia]?
        /// The what-if preview's line (the edited stretch).
        var previewLine: [RouteSnap.Coord] = []
    }

    struct Days {
        var days: [TripMultiday.Day]
        /// Frame only this day (1-based), or the whole trip when nil.
        var day: Int?
        /// The weather on each day's legs, drawn as labels while the Weather toggle is on.
        var weatherOn = false
    }

    enum Content {
        case route(OneRoute)
        case days(Days)
    }

    /// What a new load of lines is for: the route, or the days (the person's stops and the weather are drawn from what is on show).
    private enum Key: Equatable {
        case route(TripRoute)
        case days([TripMultiday.Day])
    }

    let content: Content
    var focus: Int?
    var onStop: ((Int) -> Void)?
    /// A tap on a leg's weather label.
    var onLegTag: ((RouteSnap.Drawing.LegTag) -> Void)?
    /// The layer pins (chargers, hotels, campgrounds along the route) the person asked to see.
    var along: [AlongCharger] = []
    /// The road line the layers follow once it is known, and whether it starts at the trip's own origin (see `LayerSearch.startsAtOrigin`).
    var onLine: ([RouteSnap.Coord], Bool) -> Void = { _, _ in }
    /// The radar frame under the route, when the Radar button is on.
    var radar: RadarLayer?
    var fills = false
    var bottomInset: CGFloat = 0
    var topInset: CGFloat = 0
    /// Bump to frame what is on show again (a day chip tapped, even the one already picked, after the person panned away).
    var reframe = 0
    /// Whether the map draws its own credit in its corner (false: the owner draws it where it stays in view, over its sheet).
    var showsCredit = true
    /// Edit mode of the one-route map (never on the days map): a long press drops a pin, the middle of the map moves a stop.
    var onLongPress: ((RouteSnap.Coord) -> Void)?
    var onCenter: ((RouteSnap.Coord) -> Void)?

    @Environment(AppState.self) private var state
    @State private var drawing: RouteSnap.Drawing?
    @State private var lines: [[RouteSnap.Coord]] = []
    @State private var provisional: [RouteSnap.Coord]?

    private var key: Key {
        switch content {
        case .route(let r): return .route(r.route)
        case .days(let d): return .days(d.days)
        }
    }

    private var day: Int? { if case .days(let d) = content { return d.day } else { return nil } }

    private var pendingVias: [PlanVia]? { if case .route(let r) = content { return r.pendingVias } else { return nil } }

    private var shown: RouteSnap.Drawing? {
        guard let base = drawing else { return nil }
        switch content {
        case .route(let r):
            var d = PlanRouteLogic.overlaid(base, focus: focus, along: along, legTags: PlanRouteLogic.legTags(route: r.route, weather: r.weather, line: base.pieces.flatMap(\.coords)))
            d.preview = r.previewLine.count >= 2 ? r.previewLine : nil
            if let provisional, provisional.count >= 2 {
                d.pieces = [RouteSnap.Piece(coords: provisional, speed: nil)]
                d.start = provisional.first
                d.end = provisional.last
            }
            return d
        case .days(let d):
            return PlanRouteLogic.daysShown(base, focus: focus, along: along, lines: lines, day: d.day, days: d.days, weatherOn: d.weatherOn)
        }
    }

    /// The layers follow the line on show: the whole trip, or the day the person picked.
    private func reportLine() {
        guard !lines.isEmpty else { return }
        onLine(PlanRouteLogic.layerLine(lines: lines, day: day), PlanRouteLogic.layerLineStartsAtOrigin(day: day))
    }

    var body: some View {
        PlanMapCanvas(drawing: shown, radar: radar, onStop: onStop, onLegTag: onLegTag, fills: fills, bottomInset: bottomInset, topInset: topInset, reframe: reframe, showsCredit: showsCredit, onLongPress: onLongPress, onCenter: onCenter)
            .onChange(of: day) { reportLine() }
            .task(id: key) { await load() }
            // A stop just added or removed: our own roads line (Valhalla's route through the places with the new set) at once, while the planner works.
            .task(id: pendingVias) {
                guard let pendingVias, case .route(let r) = content else { provisional = nil; return }
                let places = PlanRouteLogic.places(r.route, vias: pendingVias)
                if let resp = try? await state.client.valhallaRoute(PlanRouteLogic.routeRequest(places)), let out = PlanRouteLogic.roadLine(resp) {
                    if !Task.isCancelled, out.count > 1 { provisional = out }
                }
            }
    }

    /// The straight legs show at once; the roads replace them when they arrive.
    private func load() async {
        MapNetwork.prepare(server: state.mapServer, token: state.mapToken)
        switch content {
        case .route(let r):
            drawing = PlanRouteLogic.drawing(route: r.route, line: PlanRouteLogic.places(r.route), vias: r.vias)
            let line = await PlanRouteLogic.line(route: r.route, source: state.client)
            if !Task.isCancelled, let d = PlanRouteLogic.drawing(route: r.route, line: line, vias: r.vias) { drawing = d; lines = [line]; provisional = nil; reportLine() }
        case .days(let d):
            let routes = d.days.compactMap(\.plan.best)
            drawing = PlanRouteLogic.drawing(days: d.days, lines: routes.map { PlanRouteLogic.places($0) })
            var found: [[RouteSnap.Coord]] = []
            for r in routes { found.append(await PlanRouteLogic.line(route: r, source: state.client)) }
            if !Task.isCancelled, let dr = PlanRouteLogic.drawing(days: d.days, lines: found) { drawing = dr; lines = found; reportLine() }
        }
    }
}
