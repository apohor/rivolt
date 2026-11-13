import Foundation

/// The elevation chart's arithmetic, kept apart from the view so a test can see it (the web's plan/elevation.ts). The cursor is a share (0 to 1) of the
/// way along the route, so the chart (the server's distances) and the map (the drawn line, snapped a little differently) agree on "how far" without
/// sharing a length.
enum PlanElevation {
    static let feetPerMeter = 3.28084
    static let metersPerMile = 1609.344
    static let context = "Elevation shown for context; Rivian's estimate decides the energy."

    /// A profile worth drawing: two points or more that run forward in distance. Anything else draws no chart.
    static func usable(_ p: ElevationProfile?) -> ElevationProfile? {
        guard let p, p.points.count >= 2 else { return nil }
        var last = -Double.infinity
        for pt in p.points {
            guard pt.count == 2, pt[0].isFinite, pt[1].isFinite, pt[0] >= last else { return nil }
            last = pt[0]
        }
        return (p.points.last?[0] ?? 0) > 0 ? p : nil
    }

    static func total(_ p: ElevationProfile) -> Double { p.points.last?[0] ?? 0 }

    /// The terrain height (metres) at a share of the way along, interpolated between the two points around it.
    static func height(_ p: ElevationProfile, at share: Double) -> Double {
        let pts = p.points
        let d = min(1, max(0, share)) * total(p)
        var lo = 0, hi = pts.count - 1
        while hi - lo > 1 {
            let mid = (lo + hi) / 2
            if pts[mid][0] <= d { lo = mid } else { hi = mid }
        }
        let (d0, e0, d1, e1) = (pts[lo][0], pts[lo][1], pts[hi][0], pts[hi][1])
        return d1 == d0 ? e0 : e0 + (e1 - e0) * (d - d0) / (d1 - d0)
    }

    /// Where the stops between the ends sit, as shares (a waypoint that could not be placed is left out).
    static func stops(_ p: ElevationProfile) -> [(index: Int, share: Double)] {
        let t = total(p)
        guard t > 0 else { return [] }
        return p.waypointAtM.enumerated().compactMap { i, m in
            guard let m, i > 0, i < p.waypointAtM.count - 1 else { return nil }
            return (i, min(1, max(0, m / t)))
        }
    }

    /// "1,234 ft" or "376 m".
    static func elevation(_ meters: Double, metric: Bool) -> String {
        "\(number(metric ? meters : meters * feetPerMeter)) \(metric ? "m" : "ft")"
    }

    /// "123 mi" or "198 km".
    static func distance(_ meters: Double, metric: Bool) -> String {
        "\(number(metric ? meters / 1000 : meters / metersPerMile)) \(metric ? "km" : "mi")"
    }

    /// "123 mi · 1,234 ft" at a share, or the range ("100 to 4,321 ft") with no cursor.
    static func readout(_ p: ElevationProfile, share: Double?, metric: Bool) -> String {
        if let share { return "\(distance(share * total(p), metric: metric)) · \(elevation(height(p, at: share), metric: metric))" }
        let (lo, hi) = range(p)
        return "\(number(metric ? lo : lo * feetPerMeter)) to \(elevation(hi, metric: metric))"
    }

    static func range(_ p: ElevationProfile) -> (lo: Double, hi: Double) {
        let ys = p.points.map { $0[1] }
        return (ys.min() ?? 0, ys.max() ?? 0)
    }

    /// What VoiceOver says for the chart as a whole.
    static func label(_ p: ElevationProfile, metric: Bool) -> String {
        let (lo, hi) = range(p)
        return "Elevation along the route, \(number(metric ? lo : lo * feetPerMeter)) to \(elevation(hi, metric: metric)), over \(distance(total(p), metric: metric))"
    }

    private static func number(_ v: Double) -> String {
        let f = NumberFormatter()
        f.numberStyle = .decimal
        f.locale = Locale(identifier: "en_US")
        f.maximumFractionDigits = 0
        return f.string(from: NSNumber(value: v.rounded())) ?? "\(Int(v.rounded()))"
    }

    // MARK: On the map's line

    static func cumulative(_ path: [RouteSnap.Coord]) -> [Double] {
        var out = [0.0]
        for i in 1..<max(path.count, 1) { out.append(out[i - 1] + meters(path[i - 1], path[i])) }
        return out
    }

    /// Equirectangular metres between two points: exact enough along one route, and cheap.
    private static func meters(_ a: RouteSnap.Coord, _ b: RouteSnap.Coord) -> Double {
        let x = rad(b.lon - a.lon) * cos(rad((a.lat + b.lat) / 2))
        let y = rad(b.lat - a.lat)
        return 6_371_008.8 * (x * x + y * y).squareRoot()
    }

    private static func rad(_ d: Double) -> Double { d * .pi / 180 }

    /// The map position a share of the way along the line, or nil for a line too short to have one.
    static func point(on path: [RouteSnap.Coord], at share: Double, cumulative cum: [Double]? = nil) -> RouteSnap.Coord? {
        guard path.count >= 2 else { return nil }
        let cum = cum ?? cumulative(path)
        guard let len = cum.last, len > 0 else { return nil }
        let d = min(1, max(0, share)) * len
        var i = 1
        while i < cum.count - 1, cum[i] < d { i += 1 }
        let span = cum[i] - cum[i - 1]
        let t = span > 0 ? (d - cum[i - 1]) / span : 0
        return RouteSnap.Coord(lat: path[i - 1].lat + (path[i].lat - path[i - 1].lat) * t, lon: path[i - 1].lon + (path[i].lon - path[i - 1].lon) * t)
    }

    /// The share of the way along the line of the point on it nearest to `p`, and how far `p` is from the line (metres).
    static func share(of p: RouteSnap.Coord, on path: [RouteSnap.Coord]) -> (share: Double, offMeters: Double)? {
        guard path.count >= 2 else { return nil }
        let cum = cumulative(path)
        guard let len = cum.last, len > 0 else { return nil }
        let r = 6_371_008.8, k = cos(rad(p.lat))
        var best = (along: 0.0, off: Double.infinity)
        for i in 1..<path.count {
            let ax = rad(path[i - 1].lon - p.lon) * k * r, ay = rad(path[i - 1].lat - p.lat) * r
            let bx = rad(path[i].lon - p.lon) * k * r, by = rad(path[i].lat - p.lat) * r
            let dx = bx - ax, dy = by - ay
            let l2 = dx * dx + dy * dy
            let t = l2 > 0 ? min(1, max(0, -(ax * dx + ay * dy) / l2)) : 0
            let off = ((ax + dx * t) * (ax + dx * t) + (ay + dy * t) * (ay + dy * t)).squareRoot()
            if off < best.off { best = (cum[i - 1] + (cum[i] - cum[i - 1]) * t, off) }
        }
        return (best.along / len, best.off)
    }
}
