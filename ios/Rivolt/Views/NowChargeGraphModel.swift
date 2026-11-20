import Foundation

/// The numbers behind the Now card's charging graph (design_preview key now_charge_graph), as the web's NowChargeGraph works them out: the open
/// charge's battery from plug-in to now, the charger's power as a quieter line, and, when the card has a finish time, the way on to the target.
/// The start and now labels are the real readings (the first stored one and the ring's own figure), so the change is exactly their difference.
/// The estimate takes at most `estShare` of the session so far again in width, so a slow charge with hours left still shows its start readably.
/// Pure: no network, no views. Points are in plot space: x across 0...1, y from the bottom 0...1.
struct NowChargeGraphModel: Equatable {
    struct Pt: Equatable {
        let x: Double
        let y: Double
    }

    /// Under this much of the session (two minutes) or three samples, the card says it is collecting instead of drawing a stub.
    static let minSpan: TimeInterval = 120
    static let estShare = 0.8
    /// At most this many points per line: a path of a few dozen curves is as smooth as one of two thousand.
    static let maxPoints = 48

    let startedAt: Date
    let startShown: Int
    let nowShown: Int
    let soc: [Pt]
    let power: [Pt]
    /// The "now" dot.
    let last: Pt
    /// The target's dot (plot space) and the pill's text, when the card has a finish time.
    let estimate: (end: Pt, pill: String)?
    /// "11 kW" in the slot's format, for the legend.
    let powerNow: String?

    static func == (a: Self, b: Self) -> Bool {
        a.startedAt == b.startedAt && a.startShown == b.startShown && a.nowShown == b.nowShown && a.soc == b.soc && a.power == b.power
            && a.last == b.last && a.estimate?.end == b.estimate?.end && a.estimate?.pill == b.estimate?.pill && a.powerNow == b.powerNow
    }

    var gained: Int { nowShown - startShown }

    /// The power axis ceiling, as the web's `powerMax`.
    static func powerMax(_ peak: Double) -> Double {
        if peak <= 0 { return 12 }
        for tier in [12.0, 25, 60, 120, 200, 300, 400] where peak <= tier * 0.92 { return tier }
        return (peak * 1.2 / 50).rounded(.up) * 50
    }

    /// The kW figure in the slot's format: one decimal under 10, whole above.
    static func kw(_ v: Double) -> String { NumberText.fixed(v, v < 10 ? 1 : 0) }

    /// Nil while the charge has too little to draw (the card then says it is collecting).
    static func make(series: ChargeCurve.Series, startPct: Double?, nowPct: Double, target: Double?, etaHours: Double?, now: Date) -> NowChargeGraphModel? {
        guard series.soc.count >= 3, let first = series.soc.first, let last = series.soc.last,
              last.at.timeIntervalSince(first.at) >= minSpan else { return nil }
        let goal = target.flatMap { $0 > nowPct ? $0 : nil }
        let tLast = last.at.timeIntervalSince(first.at)
        let endAt: Date? = goal != nil && (etaHours ?? 0) > 0 ? max(last.at, now).addingTimeInterval((etaHours ?? 0) * 3600) : nil
        let tEnd = endAt.map { $0.timeIntervalSince(first.at) }
        let t1 = tEnd.map { tLast + min($0 - tLast, tLast * estShare) } ?? tLast
        let hi = max(last.value, goal ?? 0, series.soc.map(\.value).max() ?? 0)
        let lo = series.soc.map(\.value).min() ?? last.value
        let span = max(hi - lo, 4)
        let yLo = lo - span * 0.12
        let peak = series.power.map(\.value).max() ?? 0
        let pMax = powerMax(peak)
        let sx = { (t: TimeInterval) in t / t1 }
        let sy = { (v: Double) in (v - yLo) / (hi - yLo) }
        let socPts = thin(series.soc).map { Pt(x: sx($0.at.timeIntervalSince(first.at)), y: sy($0.value)) }
        let powPts = thin(series.power).map { Pt(x: sx($0.at.timeIntervalSince(first.at)), y: min(1, max(0, $0.value / pMax))) }
        let est: (end: Pt, pill: String)? = goal.map { g in
            (Pt(x: 1, y: sy(g)), "\(Int(g.rounded()))% · \((endAt ?? last.at).formatted(date: .omitted, time: .shortened))")
        }
        return NowChargeGraphModel(
            startedAt: first.at,
            startShown: Int((startPct ?? first.value).rounded()),
            nowShown: Int(nowPct.rounded()),
            soc: socPts, power: powPts,
            last: Pt(x: sx(tLast), y: sy(last.value)),
            estimate: tEnd == nil ? nil : est,
            powerNow: series.power.last.map { kw($0.value) })
    }

    /// At most `maxPoints`, evenly picked and always keeping the last.
    static func thin<T>(_ pts: [T]) -> [T] {
        guard pts.count > maxPoints else { return pts }
        var out: [T] = []
        for i in 0..<(maxPoints - 1) { out.append(pts[Int((Double(i) * Double(pts.count - 1) / Double(maxPoints - 1)).rounded())]) }
        out.append(pts[pts.count - 1])
        return out
    }

    /// The header's words: "+12%" and "since 10:30 AM"; no change shown when the battery has not gained.
    var change: String? { gained > 0 ? "+\(gained)%" : nil }
    var since: String { "since \(startedAt.formatted(date: .omitted, time: .shortened))" }

    /// For VoiceOver.
    var spoken: String {
        var s = "Battery from \(startShown) percent to \(nowShown) percent \(since)"
        if let p = powerNow { s += ", charging at \(p.replacingOccurrences(of: ".0", with: "")) kilowatts" }
        if let e = estimate { s += ", reaching \(e.pill.replacingOccurrences(of: "·", with: "at"))" }
        return s + "."
    }
}

/// A smooth monotone-cubic line through points (the web's `softPath`): no overshoot between samples.
enum NowGraphCurve {
    /// Readings that jump (a drive or charge between two readings, a wake-up) arrive a few points apart, which a curve can only draw as a corner.
    /// This spreads each such jump into a short eased S that ends on the second reading (so a dot there stays on the line) and starts level at the
    /// first one's height; `segments` then draws it with no slope at the held indices. A gentle pair is left alone, and a reading that carries a dot or
    /// label (its index in `pinned`) is never moved: the ramp starts at it instead. Same rule as the web's easeSteps.
    static func easeSteps(_ pts: [CGPoint], pinned: Set<Int> = [], minDrop: CGFloat = 3, slope: CGFloat = 1.2) -> (points: [CGPoint], hold: Set<Int>) {
        guard let first = pts.first else { return ([], []) }
        var out: [CGPoint] = [first]
        var hold = Set<Int>()
        for i in 1..<max(pts.count, 1) {
            let a = pts[i - 1]
            let b = pts[i]
            let dy = abs(b.y - a.y)
            if dy < minDrop || dy / max(b.x - a.x, 0.5) <= slope {
                out.append(b)
                continue
            }
            let run = min(max(dy * 2, 12), 28)
            let last = out[out.count - 1]
            if pinned.contains(i - 1) {
                hold.insert(out.count - 1)
                var x = b.x
                if !pinned.contains(i), i + 1 < pts.count { x = min(max(b.x, a.x + run), b.x + 0.4 * (pts[i + 1].x - b.x)) }
                hold.insert(out.count)
                out.append(CGPoint(x: x, y: b.y))
                continue
            }
            if hold.contains(out.count - 1) {
                // The first reading ends the jump before this one: it stays, and the next ramp starts level after it if there is room.
                let x = max(b.x - run, last.x)
                if x > last.x + 0.5 {
                    hold.insert(out.count)
                    out.append(CGPoint(x: x, y: a.y))
                }
            } else {
                let from = out.count > 1 ? out[out.count - 2].x : a.x
                out[out.count - 1] = CGPoint(x: max(b.x - run, from + 0.4 * (a.x - from)), y: a.y)
                hold.insert(out.count - 1)
            }
            hold.insert(out.count)
            out.append(b)
        }
        return (out, hold)
    }

    /// Control points for each segment, in the order the points come. Points whose index is in `hold` leave and arrive level.
    static func segments(_ pts: [CGPoint], hold: Set<Int> = []) -> [(c1: CGPoint, c2: CGPoint, end: CGPoint)] {
        let n = pts.count
        guard n >= 2 else { return [] }
        var dx: [CGFloat] = []
        var m: [CGFloat] = []
        for i in 0..<(n - 1) {
            dx.append(pts[i + 1].x - pts[i].x)
            m.append(dx[i] == 0 ? 0 : (pts[i + 1].y - pts[i].y) / dx[i])
        }
        var t: [CGFloat] = [m[0]]
        for i in 1..<max(n - 1, 1) where i < n - 1 { t.append(m[i - 1] * m[i] <= 0 ? 0 : (m[i - 1] + m[i]) / 2) }
        t.append(m[n - 2])
        for i in hold where i < n { t[i] = 0 }
        for i in 0..<(n - 1) {
            if m[i] == 0 { t[i] = 0; t[i + 1] = 0; continue }
            let a = t[i] / m[i]
            let b = t[i + 1] / m[i]
            let s = a * a + b * b
            if s > 9 {
                let k = 3 / s.squareRoot()
                t[i] = k * a * m[i]
                t[i + 1] = k * b * m[i]
            }
        }
        return (0..<(n - 1)).map { i in
            let h = dx[i] / 3
            return (CGPoint(x: pts[i].x + h, y: pts[i].y + t[i] * h), CGPoint(x: pts[i + 1].x - h, y: pts[i + 1].y - t[i + 1] * h), pts[i + 1])
        }
    }
}
