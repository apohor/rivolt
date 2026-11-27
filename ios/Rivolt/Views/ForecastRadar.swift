import Foundation
import Observation
import SwiftUI

/// The planner's forecast radar for a chosen hour of the trip (design key plan_forecast_map, Pro): the pure rules first (they mirror the web's
/// forecastRadar.ts and tripPure.ts, with the same table-driven vectors for `positionAt`), then the model and the scrubber.
enum ForecastRadarLogic {
    static let credit = "NOAA RRFS"
    static let maxSlots = 48
    static let linePastHorizon = "The forecast doesn't reach this hour."
    static let lineOutsideUS = "The forecast covers the contiguous US."
    static let lineDown = "Forecast unavailable"
    static let designKey = "plan_forecast_map"
    /// The scrubber settles this long before tiles are asked for: one hour at z7 is dozens of tiles, and the server allows 600 a minute.
    static let debounce: Duration = .milliseconds(300)

    struct Slot: Equatable {
        var t: Int
        var fh: Int?
    }

    /// One slot per hour on the hour, from the hour the trip leaves to the hour it arrives in (at least one, at most `maxSlots`); `fh` is nil where the
    /// forecast has no such hour (before its first or past its horizon).
    static func slots(hours: [ForecastHours.Hour], depart: Int, arrive: Int) -> [Slot] {
        guard depart > 0 else { return [] }
        let first = depart / 3600 * 3600
        let last = max(first, arrive / 3600 * 3600)
        let byTime = Dictionary(hours.map { ($0.validUnix, $0.fh) }, uniquingKeysWith: { a, _ in a })
        var out: [Slot] = []
        var t = first
        while t <= last, out.count < maxSlots {
            out.append(Slot(t: t, fh: byTime[t]))
            t += 3600
        }
        return out
    }

    /// Inside the grid's box (west, south, east, north); the route's middle decides whether the trip is in the forecast's reach.
    static func inBounds(_ b: [Double], lat: Double, lon: Double) -> Bool {
        b.count == 4 && lon >= b[0] && lat >= b[1] && lon <= b[2] && lat <= b[3]
    }

    /// The one plain line under the scrubber, or nil where the hour is drawn.
    static func line(slot: Slot?, inside: Bool) -> String? {
        if !inside { return lineOutsideUS }
        if slot?.fh == nil { return linePastHorizon }
        return nil
    }

    /// "Forecast for 3 pm", in the zone of the phone.
    static func label(unix: Int, zone: TimeZone = .current, locale: Locale = .current) -> String {
        let f = DateFormatter()
        f.locale = locale
        f.timeZone = zone
        f.setLocalizedDateFormatFromTemplate("j")
        // The system puts a narrow no-break space before "PM"; the label is plain words.
        let hour = f.string(from: Date(timeIntervalSince1970: TimeInterval(unix))).replacingOccurrences(of: "\u{202F}", with: " ").replacingOccurrences(of: "\u{00A0}", with: " ")
        return "Forecast for " + hour.lowercased()
    }

    /// "NOAA RRFS · Forecast for 3 pm".
    static func caption(credit: String?, unix: Int, zone: TimeZone = .current, locale: Locale = .current) -> String {
        "\(credit ?? Self.credit) · \(label(unix: unix, zone: zone, locale: locale))"
    }

    static func spoken(credit: String?, unix: Int, zone: TimeZone = .current, locale: Locale = .current) -> String {
        "light, moderate, heavy, \(credit ?? Self.credit) radar, \(label(unix: unix, zone: zone, locale: locale))"
    }

    /// The middle of the trip as the web takes it: the mean of the stops. nil for a plan without places.
    static func centre(_ route: TripRoute) -> (lat: Double, lon: Double)? {
        let p = route.waypoints.compactMap { w in w.latitude.flatMap { la in w.longitude.map { (la, $0) } } }
        guard !p.isEmpty else { return nil }
        return (p.map(\.0).reduce(0, +) / Double(p.count), p.map(\.1).reduce(0, +) / Double(p.count))
    }

    // MARK: Where the truck is

    private static func seconds(_ d: LenientDate?) -> Double? { d?.date?.timeIntervalSince1970 }

    /// When the trip leaves (the origin's departure) and arrives (the destination's arrival), unix seconds; 0 for a plan without times.
    static func tripTimes(_ route: TripRoute) -> (depart: Int, arrive: Int) {
        guard let first = route.waypoints.first, let last = route.waypoints.last else { return (0, 0) }
        let d = seconds(first.departureTimeUTC) ?? seconds(first.arrivalTimeUTC) ?? 0
        let a = seconds(last.arrivalTimeUTC) ?? seconds(last.departureTimeUTC) ?? 0
        return (Int(d), Int(a))
    }

    /// Where the truck is at unix time `t`: at a stop during its dwell (arrival to departure), on the road between two stops by time (linear, as the
    /// server samples the weather), nil before the trip leaves or after it arrives. Between stops it follows `line` (the road) when there is one and the
    /// stops' distances are known, else the straight line between them.
    static func positionAt(_ route: TripRoute, line: [RouteSnap.Coord], t: Double) -> RouteSnap.Coord? {
        let w = route.waypoints
        guard w.count >= 2, t.isFinite else { return nil }
        func arr(_ p: TripWaypoint) -> Double? { seconds(p.arrivalTimeUTC) ?? seconds(p.departureTimeUTC) }
        func dep(_ p: TripWaypoint) -> Double? { seconds(p.departureTimeUTC) ?? seconds(p.arrivalTimeUTC) }
        let cum = line.count > 1 ? PlanElevation.cumulative(line) : []
        let total = route.totalDriveDistanceMeters ?? 0
        for i in w.indices {
            guard let a = arr(w[i]), let d = dep(w[i]), let lat = w[i].latitude, let lon = w[i].longitude else { return nil }
            if t >= a, t <= d { return RouteSnap.Coord(lat: lat, lon: lon) }
            if i == w.count - 1 { break }
            guard let na = arr(w[i + 1]), let nlat = w[i + 1].latitude, let nlon = w[i + 1].longitude else { return nil }
            if t > d, t < na {
                let f = (t - d) / (na - d)
                let d0 = w[i].distanceFromOriginMeters ?? 0, d1 = w[i + 1].distanceFromOriginMeters ?? 0
                // 0 means missing on legacy plans; the origin's own 0 is real, so only the later stop's must be set.
                if !cum.isEmpty, total > 0, d1 > d0, let at = PlanElevation.point(on: line, at: (d0 + f * (d1 - d0)) / total, cumulative: cum) { return at }
                return RouteSnap.Coord(lat: lat + (nlat - lat) * f, lon: lon + (nlon - lon) * f)
            }
            if t < a { return nil }
        }
        return nil
    }
}

/// The forecast's state for the planner map: whether the server offers it (hours loaded), the scrubber's place and the place tiles are drawn for (it
/// settles `debounce` after the thumb stops, so a sweep never asks for the hours in between).
@MainActor
@Observable
final class ForecastRadarModel {
    enum Load: Equatable {
        case idle, loading, ready(ForecastHours)
        /// Free account, no forecast on this server, signed out, rate limited: say nothing, the live radar stays as it was.
        case hidden
        /// The feed is down: the forecast mode says so in one line.
        case down
    }

    private(set) var load = Load.idle
    /// The thumb's slot (the label and the truck follow it at once) and the slot whose tiles are drawn.
    private(set) var index = 0
    private(set) var drawnIndex = 0
    var debounce: Duration = ForecastRadarLogic.debounce
    var refreshEvery: Duration = .seconds(600)
    private var settle: Task<Void, Never>?

    var hours: ForecastHours? { if case .ready(let h) = load { h } else { nil } }

    /// Asks for the hours (once per session until something resets it; a refresh keeps the hours on show when it fails).
    func fetch(source: some ForecastSource) async {
        if case .idle = load { load = .loading }
        do {
            load = .ready(try await source.forecastHours())
        } catch APIError.server(let status, _, _) where [401, 402, 403, 404, 429].contains(status) {
            load = .hidden
        } catch {
            if hours == nil { load = .down }
        }
    }

    /// While on, the hours are asked for again every ten minutes (a new cycle lands about hourly).
    func keepFresh(source: some ForecastSource) async {
        while !Task.isCancelled {
            try? await Task.sleep(for: refreshEvery)
            if Task.isCancelled { return }
            await fetch(source: source)
        }
    }

    func select(_ i: Int) {
        index = i
        settle?.cancel()
        settle = Task { [debounce] in
            try? await Task.sleep(for: debounce)
            if !Task.isCancelled { drawnIndex = i }
        }
    }

    /// A new plan or a different number of slots: back to the departure hour, drawn at once.
    func restart() {
        settle?.cancel()
        index = 0
        drawnIndex = 0
    }

    func reset() {
        settle?.cancel()
        load = .idle
        index = 0
        drawnIndex = 0
    }

    /// Whether the forecast replaces the live radar: the key, a signed-in account (never the demo) and the Radar button on, and the server not saying
    /// "not for you". While the hours load, the live radar waits (no flash of it).
    static func replacesLive(keyOn: Bool, demo: Bool, radarOn: Bool, load: Load) -> Bool {
        guard keyOn, !demo, radarOn else { return false }
        if case .hidden = load { return false }
        return true
    }

    /// The layer to draw: the settled slot's hour; nil for a slot the forecast does not have or a trip outside the grid.
    func layer(server: URL, slots: [ForecastRadarLogic.Slot], inside: Bool) -> RadarLayer? {
        guard case .ready(let h) = load, inside, slots.indices.contains(drawnIndex), let fh = slots[drawnIndex].fh else { return nil }
        return .forecast(server: server, cycle: h.cycle, fh: fh)
    }
}

/// The scrubber on the sheet's edge: one step is one hour, from the hour the trip leaves to the hour it arrives in, with the one plain line under it
/// where the hour is not drawn.
struct ForecastScrubber: View {
    let model: ForecastRadarModel
    let slotCount: Int
    let valueText: String
    let line: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            if slotCount > 1 {
                Slider(value: Binding(get: { Double(model.index) }, set: { model.select(Int($0.rounded())) }), in: 0...Double(slotCount - 1), step: 1)
                    .tint(Theme.accent)
                    .accessibilityIdentifier("forecast-slider")
                    .accessibilityLabel("Forecast hour")
                    .accessibilityValue(valueText)
            }
            if let line {
                Text(line).mapCaption().foregroundStyle(Theme.textSecondary).lineLimit(2)
                    .accessibilityIdentifier("forecast-line")
            }
        }
        .padding(.horizontal, 12).padding(.vertical, 6)
        .background(Theme.card.opacity(0.92), in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(Theme.border, lineWidth: 1))
    }
}
