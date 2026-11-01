import Foundation

/// The drive map's colour-by-metric maths, pure functions with no map library: which range of a metric the colour ramp is scaled to, the
/// gradient stops MapLibre's `line-gradient` takes, and the legend's two end labels. A port of the web's `web/src/map/metrics.ts`
/// (`percentileRange`, `hasSpread`, `gradientStops`, `formatValue`), with the same rules and the same test cases, so the two maps colour a drive
/// alike. The colour ramp itself, the series per sample and the wiring are elsewhere.
enum RouteGradient {
    /// What a drive can be coloured by. Values are in the units the server sends: speed in mph, efficiency in mi/kWh, elevation in metres.
    enum Metric: Hashable, CaseIterable {
        case speed, efficiency, elevation
    }

    /// The account's distance unit: it decides the legend's units (mph or km/h, mi/kWh or km/kWh, ft or m).
    enum DistanceUnit: Equatable {
        case miles, kilometres
    }

    /// A metric whose 5th-to-95th percentile range is nearly flat has nothing to show (the whole line would take the ramp's first colour and the
    /// legend would read "2.5 to 2.5"): efficiency needs a spread of at least 5% of its mean, elevation of 10 m. Speed has no minimum.
    static let efficiencyMinimumSpread = 0.05
    static let elevationMinimumSpreadMetres = 10.0

    // MARK: Range

    /// The 5th to 95th percentile of the finite values (linear interpolation), so one spike does not wash out the rest. nil when there is nothing
    /// to scale to: no finite value, or a flat range (the 5th and 95th percentile are the same).
    static func range(values: [Double]) -> (lo: Double, hi: Double)? {
        let v = values.filter { $0.isFinite }.sorted()
        guard !v.isEmpty else { return nil }
        func at(_ p: Double) -> Double {
            let x = p / 100 * Double(v.count - 1)
            let i = Int(x.rounded(.down))
            let j = min(v.count - 1, i + 1)
            return v[i] + (v[j] - v[i]) * (x - Double(i))
        }
        let lo = at(5), hi = at(95)
        return hi > lo ? (lo, hi) : nil
    }

    /// `range(values:)`, and nil too when the spread is under the metric's minimum (the rule that hides a colour mode on a drive that does not
    /// vary): efficiency under 5% of the mean's magnitude, elevation under 10 m.
    static func range(values: [Double], metric: Metric) -> (lo: Double, hi: Double)? {
        guard let r = range(values: values) else { return nil }
        let spread = r.hi - r.lo
        switch metric {
        case .speed:
            return r
        case .efficiency:
            let f = values.filter { $0.isFinite }
            let mean = abs(f.reduce(0, +) / Double(f.count))
            return spread >= efficiencyMinimumSpread * mean ? r : nil
        case .elevation:
            return spread >= elevationMinimumSpreadMetres ? r : nil
        }
    }

    // MARK: Stops

    /// The stops of a `line-gradient`: the value at `count` + 1 evenly spaced offsets along the line, interpolated from the per-vertex `values`
    /// over the vertices' `progress` (0 at the first vertex to 1 at the last, never decreasing; a repeated progress is fine), and scaled to
    /// `t` in 0...1 against lo...hi (clamped, so one value far above hi is simply the top of the ramp). A value that is not finite counts as `lo`.
    /// Offsets run strictly increasing from 0 to 1, with no repeats, as MapLibre requires. A line with no length, or no values, is one flat
    /// colour (t = 0.5) over two stops.
    static func stops(progress: [Double], values: [Double], lo: Double, hi: Double, count: Int = 90) -> [(offset: Double, t: Double)] {
        let n = max(1, count)
        let flat: [(offset: Double, t: Double)] = [(0, 0.5), (1, 0.5)]
        guard let first = progress.first, let last = progress.last, last > first, !values.isEmpty else { return flat }
        let span = last - first
        func scaled(_ v: Double) -> Double {
            guard hi > lo else { return 0.5 }
            return min(1, max(0, ((v.isFinite ? v : lo) - lo) / (hi - lo)))
        }
        var out: [(offset: Double, t: Double)] = []
        out.reserveCapacity(n + 1)
        var j = 0
        for k in 0...n {
            let p = Double(k) / Double(n)
            let target = first + p * span
            while j < progress.count - 2 && progress[j + 1] < target { j += 1 }
            let a = progress[j]
            let b = j + 1 < progress.count ? progress[j + 1] : a
            let f = b > a ? min(1, max(0, (target - a) / (b - a))) : 0
            let va = j < values.count ? values[j] : values[values.count - 1]
            let vb = j + 1 < values.count ? values[j + 1] : va
            let v: Double
            if va.isFinite && vb.isFinite {
                v = va + (vb - va) * f
            } else {
                v = va.isFinite ? va : vb
            }
            out.append((p, scaled(v)))
        }
        return out
    }

    // MARK: Legend

    /// The legend's two end labels, in the account's units: for example "32 mph" and "55 mph", or "3.4 mi/kWh" and "5.1 mi/kWh".
    static func legend(lo: Double, hi: Double, metric: Metric, unit: DistanceUnit = .miles) -> (min: String, max: String) {
        (format(lo, metric: metric, unit: unit), format(hi, metric: metric, unit: unit))
    }

    private static let milesPerKilometre = 0.6213711922
    private static let feetPerMetre = 3.28084

    /// A value in the account's units with its unit: speed rounded (mph, or km/h), efficiency to one decimal (mi/kWh, or km/kWh), elevation
    /// rounded with thousands separators (ft, or m). "—" for a value that is not finite.
    static func format(_ v: Double, metric: Metric, unit: DistanceUnit) -> String {
        guard v.isFinite else { return "—" }
        switch metric {
        case .speed:
            let n = jsRound(unit == .miles ? v : v / milesPerKilometre)
            return "\(Int(n)) \(unit == .miles ? "mph" : "km/h")"
        case .efficiency:
            // v is mi/kWh; in km units it is km/kWh.
            let n = (unit == .miles ? v : v / milesPerKilometre)
            return String(format: "%.1f", jsRound(n * 10) / 10) + (unit == .miles ? " mi/kWh" : " km/kWh")
        case .elevation:
            let n = jsRound(unit == .miles ? v * feetPerMetre : v)
            return grouped(Int(n)) + (unit == .miles ? " ft" : " m")
        }
    }

    /// JavaScript's Math.round (ties go up, toward +infinity), which the web's labels use: a negative half rounds toward zero, not away from it.
    private static func jsRound(_ x: Double) -> Double { (x + 0.5).rounded(.down) }

    /// 1234 as "1,234" (en-US), -1234 as "-1,234".
    private static func grouped(_ n: Int) -> String {
        let digits = String(abs(n))
        var out = ""
        for (i, c) in digits.reversed().enumerated() {
            if i > 0 && i % 3 == 0 { out.append(",") }
            out.append(c)
        }
        return (n < 0 ? "-" : "") + String(out.reversed())
    }
}
