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

    /// Valhalla's route through the places: the start and the destination are hard stops, the chargers are passed through so
    /// a charger the roads can't snap to doesn't fail the route (as the web does).
    static func routeRequest(_ places: [RouteSnap.Coord]) -> ValhallaRouteRequest {
        ValhallaRouteRequest(locations: places.enumerated().map { i, c in
            .init(lat: c.lat, lon: c.lon, type: i == 0 || i == places.count - 1 ? "break" : "through")
        })
    }

    /// The drawing for a line: no speeds, the first and last points as the start and end, a pin for every charging stop.
    static func drawing(route: TripRoute, line: [RouteSnap.Coord]) -> RouteSnap.Drawing? {
        guard line.count >= 2 else { return nil }
        let stops = route.chargingStops.compactMap { s -> RouteSnap.Drawing.Stop? in
            guard let la = s.latitude, let lo = s.longitude else { return nil }
            return .init(coord: RouteSnap.Coord(lat: la, lon: lo), label: stopLabel(s))
        }
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
        if let r = try? await source.valhallaRoute(routeRequest(places)), let legs = r.trip?.legs, !legs.isEmpty {
            var out: [RouteSnap.Coord] = []
            for (i, leg) in legs.enumerated() {
                var seg = leg.shape.map { DriveRoute.decode($0, precision: 6) } ?? []
                if i > 0, !seg.isEmpty { seg.removeFirst() }
                out += seg
            }
            if out.count > 1 { return out }
        }
        return places
    }

    /// Both days of a two-day trip as one drawing: each day's line, a pin for every charging stop and one for each night.
    static func drawing(days: [TripMultiday.Day], lines: [[RouteSnap.Coord]]) -> RouteSnap.Drawing? {
        let pieces = lines.filter { $0.count >= 2 }.map { RouteSnap.Piece(coords: $0, speed: nil) }
        guard let first = pieces.first?.coords.first, let last = pieces.last?.coords.last else { return nil }
        var stops: [RouteSnap.Drawing.Stop] = []
        for day in days {
            for s in day.plan.best?.chargingStops ?? [] {
                guard let la = s.latitude, let lo = s.longitude else { continue }
                stops.append(.init(coord: RouteSnap.Coord(lat: la, lon: lo), label: stopLabel(s)))
            }
            if day.overnight != nil, let la = day.end?.latitude, let lo = day.end?.longitude {
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
    var onStop: ((Int) -> Void)?

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    @State private var health = MapHealth.ok

    var body: some View {
        ZStack {
            if health == .failed {
                MapMessage(text: "Map unavailable.")
            } else if let drawing, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                RouteMapView(styleJSON: style, drawing: drawing, onHealth: { health = $0 }, onStop: onStop)
                    .accessibilityElement()
                    .accessibilityLabel("Map of the planned route with \(drawing.stops.count) \(drawing.stops.count == 1 ? "pin" : "pins")")
                    .overlay(alignment: .bottomTrailing) { MapAttribution() }
                    .overlay { if health == .noTiles { MapUnavailableOverlay() } }
            } else {
                ProgressView()
            }
        }
        .frame(maxWidth: .infinity)
        .frame(height: 240)
        .background(Theme.card)
        .clipShape(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
    }
}

/// The plan's route: the line, the start and the destination, and a pin for each charging stop with its charge-to and minutes. A tap
/// on a pin goes to that stop in the list. No location services.
struct PlanRouteMap: View {
    let route: TripRoute
    let onStop: (Int) -> Void

    @Environment(AppState.self) private var state
    @State private var drawing: RouteSnap.Drawing?

    var body: some View {
        PlanMapCanvas(drawing: drawing, onStop: onStop)
            .task(id: route) {
                MapNetwork.prepare(server: state.mapServer, token: state.mapToken)
                // The straight legs show at once; the roads replace them when they arrive.
                drawing = PlanRouteLogic.drawing(route: route, line: PlanRouteLogic.places(route))
                let line = await PlanRouteLogic.line(route: route, source: state.client)
                if !Task.isCancelled, let d = PlanRouteLogic.drawing(route: route, line: line) { drawing = d }
            }
    }
}

/// Both days of a two-day trip, with a pin where each night is spent.
struct PlanDaysMap: View {
    let days: [TripMultiday.Day]

    @Environment(AppState.self) private var state
    @State private var drawing: RouteSnap.Drawing?

    var body: some View {
        PlanMapCanvas(drawing: drawing)
            .task(id: days) {
                MapNetwork.prepare(server: state.mapServer, token: state.mapToken)
                let routes = days.compactMap(\.plan.best)
                drawing = PlanRouteLogic.drawing(days: days, lines: routes.map { PlanRouteLogic.places($0) })
                var lines: [[RouteSnap.Coord]] = []
                for r in routes { lines.append(await PlanRouteLogic.line(route: r, source: state.client)) }
                if !Task.isCancelled, let d = PlanRouteLogic.drawing(days: days, lines: lines) { drawing = d }
            }
    }
}
