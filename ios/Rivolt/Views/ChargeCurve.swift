import Foundation

/// The numbers behind a charge's curve, as the web's charge page works them out: the battery level and the charger's
/// power over the session (smoothed, since the level ticks in 1% steps and the power jitters), the pack temperature as
/// an opt-in overlay, and the axes. Pure: no network, no views.
enum ChargeCurve {
    struct Pt: Equatable {
        let at: Date
        let value: Double
    }

    struct Series: Equatable {
        var soc: [Pt] = []
        var power: [Pt] = []
        var packAvgC: [Pt] = []
        /// The widest gap between the hottest and the coolest cell in any sample, in °C; 0 when not reported.
        var packSpreadC: Double = 0
    }

    /// The left axis is the battery level over the session's own range; the right one the power, up to a tier.
    struct Axes: Equatable {
        let socLo: Double
        let socHi: Double
        let powerMax: Double
    }

    /// A Gaussian-weighted moving average over a time window (`sigma` seconds, one standard deviation), the web's
    /// `smoothGaussianTime`: it keeps peaks and copes with samples that are unevenly spaced. Fewer than three points
    /// come back as they are.
    static func smooth(_ pts: [Pt], sigma: TimeInterval) -> [Pt] {
        guard pts.count >= 3, sigma > 0 else { return pts }
        let cutoff = sigma * 3
        let twoSigmaSq = 2 * sigma * sigma
        let n = pts.count
        var out: [Pt] = []
        out.reserveCapacity(n)
        var lo = 0
        var hi = 0
        for i in 0..<n {
            let t = pts[i].at
            while lo < n, pts[lo].at < t.addingTimeInterval(-cutoff) { lo += 1 }
            if hi < lo { hi = lo }
            while hi < n, pts[hi].at <= t.addingTimeInterval(cutoff) { hi += 1 }
            var weights = 0.0
            var sum = 0.0
            for j in lo..<hi {
                let dt = pts[j].at.timeIntervalSince(t)
                let w = exp(-(dt * dt) / twoSigmaSq)
                weights += w
                sum += w * pts[j].value
            }
            out.append(Pt(at: t, value: weights > 0 ? sum / weights : pts[i].value))
        }
        return out
    }

    /// The samples of `[start, end + 60 s]` as three series. A battery level of 0 or less and a power of 0 or less
    /// are "not reported", as on the web, and a temperature of 0 is the missing sentinel.
    static func series(_ samples: [ChargeSample], from start: Date, to end: Date) -> Series {
        let window = samples
            .filter { $0.at >= start && $0.at <= end.addingTimeInterval(60) }
            .sorted { $0.at < $1.at }
        let soc = window.compactMap { $0.batteryLevelPct.isFinite && $0.batteryLevelPct > 0 ? Pt(at: $0.at, value: $0.batteryLevelPct) : nil }
        let power = window.compactMap { $0.chargerPowerKw.isFinite && $0.chargerPowerKw > 0 ? Pt(at: $0.at, value: $0.chargerPowerKw) : nil }
        let pack = window.compactMap { s -> Pt? in
            guard let v = s.packTempAvgC, v.isFinite, v != 0 else { return nil }
            return Pt(at: s.at, value: v)
        }
        let spread = window.compactMap { s -> Double? in
            guard let hi = s.packTempMaxC, let lo = s.packTempMinC, hi.isFinite, lo.isFinite, hi > lo else { return nil }
            return hi - lo
        }.max() ?? 0
        return Series(soc: smooth(soc, sigma: 30), power: smooth(power, sigma: 45), packAvgC: smooth(pack, sigma: 60), packSpreadC: spread)
    }

    /// The power axis ceiling, snapped to the common charger classes (a 7 kW home session and a 250 kW pull both look
    /// right) with a little headroom; `peak` is the session's own maximum.
    static func powerAxisMax(peak: Double, smoothed: [Pt]) -> Double {
        var top = peak
        for p in smoothed where p.value > top { top = p.value }
        if top <= 0 { return 12 }
        for tier in [12.0, 25, 60, 120, 200, 300, 400] where top <= tier * 0.92 { return tier }
        return (top * 1.2 / 50).rounded(.up) * 50
    }

    /// The battery axis: the session's start and end levels with 5 points of room, or the data's own range when the
    /// row doesn't say.
    static func axes(_ series: Series, startPct: Double, endPct: Double, peakKw: Double) -> Axes {
        var lo = startPct > 0 ? max(0, startPct - 5) : (series.soc.map(\.value).min() ?? 0)
        var hi = endPct > 0 ? min(100, endPct + 5) : (series.soc.map(\.value).max() ?? 100)
        if hi - lo < 10 {
            lo = max(0, lo - 5)
            hi = min(100, hi + 5)
        }
        if hi <= lo { lo = 0; hi = 100 }
        return Axes(socLo: lo, socHi: hi, powerMax: powerAxisMax(peak: peakKw, smoothed: series.power))
    }

    // MARK: Plot space

    /// Every series is drawn in 0...1: the battery over its axis, the power over its own, the temperature over the
    /// power's range with a margin (the web maps it into the right axis the same way).
    static func unit(soc: Double, _ axes: Axes) -> Double { clamp((soc - axes.socLo) / (axes.socHi - axes.socLo)) }
    static func unit(power: Double, _ axes: Axes) -> Double { clamp(power / axes.powerMax) }
    static func unit(temp: Double, range: ClosedRange<Double>) -> Double {
        let span = max(1, range.upperBound - range.lowerBound)
        return 0.05 + 0.9 * clamp((temp - range.lowerBound) / span)
    }

    private static func clamp(_ v: Double) -> Double { min(1, max(0, v)) }

    /// The temperature range of a series, for `unit(temp:range:)`.
    static func tempRange(_ pts: [Pt]) -> ClosedRange<Double> {
        let ys = pts.map(\.value)
        return (ys.min() ?? 0)...(ys.max() ?? 1)
    }

    /// The value of the point closest in time to `at`, or nil with none: the readout under the finger.
    static func nearest(_ pts: [Pt], to at: Date) -> Double? {
        pts.min { abs($0.at.timeIntervalSince(at)) < abs($1.at.timeIntervalSince(at)) }?.value
    }

    /// The temperature in the phone's unit.
    static func temperature(_ c: Double, metric: Bool) -> Double { metric ? c : c * 1.8 + 32 }

    /// The curve as sentences for VoiceOver: the level at the start and the end, and the peak power.
    static func spoken(_ series: Series, peakKw: Double) -> String {
        guard let first = series.soc.first, let last = series.soc.last else { return "No battery readings." }
        var s = "Battery from \(Int(first.value.rounded())) percent to \(Int(last.value.rounded())) percent"
        let minutes = last.at.timeIntervalSince(first.at) / 60
        if minutes >= 1 { s += " over \(ChartMath.spokenMinutes(minutes))" }
        s += "."
        let peak = max(peakKw, series.power.map(\.value).max() ?? 0)
        if peak > 0 { s += " Peak power \(NumberText.fixed(peak, 0)) kilowatts." }
        return s
    }
}
