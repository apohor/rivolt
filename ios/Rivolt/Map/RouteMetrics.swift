import Foundation

/// The drive map's other colour modes, Efficiency and Elevation, as pure functions: a port of the web's `web/src/map/metrics.ts` (same windows,
/// thresholds and caps), so both maps colour a drive alike. The series are one value per sample, in the units the server sends (efficiency in
/// mi/kWh, elevation in metres); `RouteGradient` scales them and writes the legend's numbers.
enum RouteMetrics {
    /// One sample of the drive as the metrics see it: position, and what each mode needs (nil where the frame had none).
    struct Sample: Equatable {
        var lat: Double
        var lon: Double
        var alt: Double?
        var soc: Double?
        var odo: Double?
    }

    /// Efficiency needs a real drive and a real battery change; elevation needs nearly every sample to carry an altitude.
    static let efficiencyMinimumMiles = 5.0
    static let efficiencyMinimumSocChange = 5.0
    static let elevationMinimumCoverage = 0.8

    private static func finite(_ v: Double?) -> Double? { v.flatMap { $0.isFinite ? $0 : nil } }

    static func haversineMi(_ a: Sample, _ b: Sample) -> Double {
        let r = 3958.8, rad = Double.pi / 180
        let dLa = (b.lat - a.lat) * rad, dLo = (b.lon - a.lon) * rad
        let x = pow(sin(dLa / 2), 2) + cos(a.lat * rad) * cos(b.lat * rad) * pow(sin(dLo / 2), 2)
        return 2 * r * asin(min(1, x.squareRoot()))
    }

    /// The distance along the samples, in miles, 0 at the first.
    static func cumulativeMiles(_ s: [Sample]) -> [Double] {
        var out = [0.0]
        for i in 1..<max(1, s.count) { out.append(out[i - 1] + haversineMi(s[i - 1], s[i])) }
        return out
    }

    /// A moving mean over ± `halfWindow` miles of the cumulative distance (values that are not finite are skipped; NaN where none is left).
    static func smooth(_ vals: [Double], cum: [Double], halfWindow: Double) -> [Double] {
        var out = [Double](repeating: .nan, count: vals.count)
        var lo = 0, hi = 0
        for i in 0..<vals.count {
            while cum[i] - cum[lo] > halfWindow { lo += 1 }
            while hi < vals.count - 1 && cum[hi + 1] - cum[i] <= halfWindow { hi += 1 }
            var sum = 0.0, n = 0.0
            for j in lo...max(lo, hi) where vals[j].isFinite { sum += vals[j]; n += 1 }
            out[i] = n > 0 ? sum / n : .nan
        }
        return out
    }

    /// Efficiency at each sample in mi/kWh, from the battery's change against the odometer. Each sample looks at a window around it of at least
    /// 1 mi that grows until the battery has moved by about 0.4 kWh (the percent steps are coarse) or it spans 6 mi. Nil when the drive does not
    /// qualify: under 5 mi, a battery change under 5 points, or no pack size, battery or odometer values to work from.
    static func efficiencySeries(_ s: [Sample], packKWh: Double?, cum: [Double]) -> [Double]? {
        guard let pack = finite(packKWh), pack > 0, s.count >= 3 else { return nil }
        let soc = s.map { finite($0.soc) }, odo = s.map { finite($0.odo) }
        guard !soc.contains(where: { $0 == nil }), !odo.contains(where: { $0 == nil }) else { return nil }
        let socs = soc.compactMap { $0 }, odos = odo.compactMap { $0 }
        guard odos[odos.count - 1] - odos[0] >= efficiencyMinimumMiles, abs(socs[0] - socs[socs.count - 1]) >= efficiencyMinimumSocChange else { return nil }
        let minWindow = 1.0, maxWindow = 6.0, minKWh = 0.4, cap = 9.0
        var out = [Double](repeating: .nan, count: s.count)
        for i in 0..<s.count {
            var lo = i, hi = i
            while true {
                let span = odos[hi] - odos[lo]
                let kwh = (socs[lo] - socs[hi]) / 100 * pack
                if (span >= minWindow && abs(kwh) >= minKWh) || span >= maxWindow { break }
                let canLo = lo > 0, canHi = hi < s.count - 1
                if !canLo && !canHi { break }
                // Grow on the side that is nearer in distance, so the window stays centred.
                if canLo && (!canHi || cum[i] - cum[lo - 1] <= cum[hi + 1] - cum[i]) { lo -= 1 } else { hi += 1 }
            }
            let span = odos[hi] - odos[lo]
            let kwh = (socs[lo] - socs[hi]) / 100 * pack
            out[i] = kwh > 0.05 && span > 0 ? min(cap, span / kwh) : (span > 0 ? cap : .nan)
        }
        // Fill any gap (a window with no movement) from its neighbours.
        for i in 0..<out.count where !out[i].isFinite {
            var j = i - 1
            while j >= 0 && !out[j].isFinite { j -= 1 }
            var k = i + 1
            while k < out.count && !out[k].isFinite { k += 1 }
            out[i] = j >= 0 ? out[j] : (k < out.count ? out[k] : cap)
        }
        return smooth(out, cum: cum, halfWindow: 0.5)
    }

    /// Elevation at each sample in metres, smoothed over ±0.4 mi; nil unless 80% of the samples carry an altitude.
    static func elevationSeries(_ s: [Sample], cum: [Double]) -> [Double]? {
        guard s.count >= 3 else { return nil }
        let have = s.filter { finite($0.alt) != nil }.count
        guard Double(have) / Double(s.count) >= elevationMinimumCoverage else { return nil }
        return smooth(s.map { finite($0.alt) ?? .nan }, cum: cum, halfWindow: 0.4)
    }

    /// The series of the modes the drive supports, each only when its range has a spread worth colouring (`RouteGradient.range`).
    static func series(_ s: [Sample], packKWh: Double?) -> [RouteGradient.Metric: [Double]] {
        let cum = cumulativeMiles(s)
        var out: [RouteGradient.Metric: [Double]] = [:]
        if let e = efficiencySeries(s, packKWh: packKWh, cum: cum), RouteGradient.range(values: e, metric: .efficiency) != nil { out[.efficiency] = e }
        if let e = elevationSeries(s, cum: cum), RouteGradient.range(values: e, metric: .elevation) != nil { out[.elevation] = e }
        return out
    }

    /// The pack's size as this drive's own figures imply it (energy used over the share of the battery it took), so the map's efficiency agrees with
    /// the drive's headline mi/kWh. Nil when the drive used under 5 points of battery.
    static func packKWh(energyUsedKWh: Double?, startSoc: Double?, endSoc: Double?) -> Double? {
        guard let e = finite(energyUsedKWh), let a = finite(startSoc), let b = finite(endSoc), e > 0, a - b >= 5 else { return nil }
        return e / ((a - b) / 100)
    }
}
