import Foundation

/// The "Estimated usable capacity" chart's rules, the web's (web/src/lib/capacityChart.ts): one dot per charge
/// estimate (no line between them, which would invent data across empty months), weighted by how much the charge
/// added, a rolling median only where estimates are dense, and the median ±15 % as the y range so a few kWh of noise
/// don't read as degradation. Every figure is an estimate, never a measurement.
enum CapacityMath {
    struct Dot: Equatable, Identifiable {
        let at: Date
        /// Clamped into the y range; `outside` when the estimate was beyond it.
        let kwh: Double
        let rawKwh: Double
        let strength: Double
        let energyKwh: Double?
        let outside: Bool
        var id: Date { at }
    }

    static let windowDays = 15.0
    static let minPoints = 3
    static let gapDays = 30.0
    static let span = 0.15

    /// How much a dot is trusted, 0.3 to 1: a charge adding 60 % of the pack or more is full strength; without the
    /// energy, a middling 0.6.
    static func strength(_ energyKwh: Double?, median: Double) -> Double {
        guard let e = energyKwh, e.isFinite, median > 0 else { return 0.6 }
        return min(1, max(0.3, e / (0.6 * median)))
    }

    static func radius(_ s: Double) -> Double { 2.5 + 3 * s }
    static func opacity(_ s: Double) -> Double { 0.25 + 0.75 * s }

    static func yDomain(median: Double) -> ClosedRange<Double> {
        (median * (1 - span)).rounded(.down)...(median * (1 + span)).rounded(.up)
    }

    static func dots(_ samples: [TruckTrends.Capacity.Sample], median: Double) -> [Dot] {
        let d = yDomain(median: median)
        return samples.filter { $0.kwhEffective.isFinite }.map { s in
            Dot(at: s.at, kwh: min(d.upperBound, max(d.lowerBound, s.kwhEffective)), rawKwh: s.kwhEffective,
                strength: strength(s.energyKwh, median: median), energyKwh: s.energyKwh,
                outside: s.kwhEffective < d.lowerBound || s.kwhEffective > d.upperBound)
        }
        .sorted { $0.at < $1.at }
    }

    struct TrendPoint: Equatable {
        let at: Date
        let kwh: Double
    }

    private static func median(_ v: [Double]) -> Double {
        let s = v.sorted(), m = s.count / 2
        return s.count % 2 == 1 ? s[m] : (s[m - 1] + s[m]) / 2
    }

    /// The rolling median as separate runs: a point only where at least `minPoints` estimates lie within
    /// ±`windowDays`, and a new run after any gap longer than `gapDays` (or a sparse point).
    static func trendRuns(_ samples: [TruckTrends.Capacity.Sample]) -> [[TrendPoint]] {
        let pts = samples.filter { $0.kwhEffective.isFinite }.sorted { $0.at < $1.at }
        var runs: [[TrendPoint]] = [], run: [TrendPoint] = []
        for (i, p) in pts.enumerated() {
            let near = pts.filter { abs($0.at.timeIntervalSince(p.at)) <= windowDays * 86_400 }.map(\.kwhEffective)
            let gap = i > 0 && p.at.timeIntervalSince(pts[i - 1].at) > gapDays * 86_400
            if gap || near.count < minPoints {
                if run.count >= 2 { runs.append(run) }
                run = []
            }
            if near.count >= minPoints { run.append(TrendPoint(at: p.at, kwh: median(near))) }
        }
        if run.count >= 2 { runs.append(run) }
        return runs
    }

    /// The rated pack is drawn only when it falls inside the calm range (a reference, not a verdict).
    static func specInRange(_ spec: Double, median: Double) -> Bool {
        spec > 0 && yDomain(median: median).contains(spec)
    }

    /// The dot nearest a date (a tap on the chart).
    static func nearest(_ dots: [Dot], to date: Date) -> Dot? {
        dots.min { abs($0.at.timeIntervalSince(date)) < abs($1.at.timeIntervalSince(date)) }
    }
}
