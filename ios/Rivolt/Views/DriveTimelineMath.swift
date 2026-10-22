import Foundation

/// The web's drive timeline maths (web/src/lib/driveTimelineData.ts, power.ts, smooth.ts), ported as is so the phone
/// and the web draw the same drive: speed, a power trace modelled from speed and grade (the truck sends no power),
/// battery %, drive mode and the moments. Times are epoch milliseconds, as on the web.
enum DriveTimelineMath {
    struct Pt: Equatable {
        var x: Double
        var y: Double
    }

    /// A modelled power point (kW, regen below 0); `gap` marks the first point after a hole in the samples.
    struct PowerPt: Equatable {
        var x: Double
        var y: Double
        var gap = false
    }

    static func ms(_ d: Date) -> Double { d.timeIntervalSince1970 * 1000 }

    // MARK: smooth.ts

    /// Gaussian-weighted moving average over a time window (sigma in ms), for irregular sampling.
    static func smoothGaussianTime(_ pts: [Pt], sigmaMs: Double) -> [Pt] {
        guard pts.count >= 3, sigmaMs > 0 else { return pts }
        let cutoff = sigmaMs * 3, twoSigmaSq = 2 * sigmaMs * sigmaMs
        var out = pts
        var lo = 0, hi = 0
        for i in pts.indices {
            let t = pts[i].x
            while lo < pts.count && pts[lo].x < t - cutoff { lo += 1 }
            if hi < lo { hi = lo }
            while hi < pts.count && pts[hi].x <= t + cutoff { hi += 1 }
            var wSum = 0.0, ySum = 0.0
            for j in lo..<hi {
                let dt = pts[j].x - t
                let w = exp(-(dt * dt) / twoSigmaSq)
                wSum += w
                ySum += w * pts[j].y
            }
            out[i] = Pt(x: t, y: wSum > 0 ? ySum / wSum : pts[i].y)
        }
        return out
    }

    // MARK: driveTimelineData.ts / power.ts

    /// Speed (mph), with a zero a second after the last sample when the drive ends moving.
    static func speedPts(_ s: [DriveSample]) -> [Pt] {
        var raw = s.map { Pt(x: ms($0.at), y: $0.speedMph ?? 0) }
        if let last = raw.last, last.y > 0.5 { raw.append(Pt(x: last.x + 1000, y: 0)) }
        return raw
    }

    /// Battery % smoothed over 30 s; a missing or non-positive reading isn't 0 %.
    static func socPts(_ s: [DriveSample]) -> [Pt] {
        smoothGaussianTime(s.compactMap { p in p.batteryLevelPct.flatMap { $0 > 0 ? Pt(x: ms(p.at), y: $0) : nil } }, sigmaMs: 30_000)
    }

    /// Elevation in feet, smoothed over 15 s.
    static func elevPts(_ s: [DriveSample]) -> [Pt] {
        smoothGaussianTime(s.compactMap { p in p.altitudeM.flatMap { $0.isFinite ? Pt(x: ms(p.at), y: $0 * 3.28084) : nil } }, sigmaMs: 15_000)
    }

    static let minDtSec = 0.5
    static let maxDtSec = 30.0

    /// Battery-side power modelled from speed and grade for a ~3 t truck, calibrated to the drive's recorded energy
    /// (factor clamped to 0.5–2) and smoothed over 4 s.
    static func power(_ s: [DriveSample], elev: [Pt], energyKwh: Double) -> [PowerPt] {
        guard s.count >= 3 else { return [] }
        let mass = 3050.0, cd = 0.34, area = 3.0, rho = 1.225, crr = 0.012, g = 9.81
        let etaDrive = 0.85, etaRegen = 0.7, accessoryKW = 1.2
        func elevAt(_ t: Double) -> Double {
            guard var best = elev.first else { return 0 }
            for p in elev where abs(p.x - t) < abs(best.x - t) { best = p }
            return best.y / 3.28084
        }
        let points = s.map { (t: ms($0.at), v: ($0.speedMph ?? 0) * 0.44704, elev: elevAt(ms($0.at))) }
        var out: [PowerPt] = []
        var pendingGap = true
        for i in 1..<points.count {
            let dtSec = (points[i].t - points[i - 1].t) / 1000
            if dtSec < minDtSec || dtSec > maxDtSec {
                pendingGap = true
                continue
            }
            let v = (points[i].v + points[i - 1].v) / 2
            let dvdt = (points[i].v - points[i - 1].v) / dtSec
            let dist = v * dtSec
            let grade = dist > 0.5 ? (points[i].elev - points[i - 1].elev) / dist : 0
            let fTotal = 0.5 * cd * area * rho * v * v + (v > 0.5 ? crr * mass * g : 0) + mass * g * grade + mass * dvdt
            let wheels = fTotal * v
            let battery = wheels >= 0 ? wheels / etaDrive + accessoryKW * 1000 : wheels * etaRegen + accessoryKW * 1000
            out.append(PowerPt(x: (points[i].t + points[i - 1].t) / 2, y: battery / 1000, gap: pendingGap))
            pendingGap = false
        }
        if energyKwh > 0 && out.count > 1 {
            var e = 0.0
            for i in 1..<out.count where !out[i].gap {
                e += (out[i].y + out[i - 1].y) / 2 * (out[i].x - out[i - 1].x) / 3_600_000
            }
            if e > 0.1 {
                let factor = max(0.5, min(2.0, energyKwh / e))
                for i in out.indices { out[i].y *= factor }
            }
        }
        let smoothed = smoothGaussianTime(out.map { Pt(x: $0.x, y: $0.y) }, sigmaMs: 4_000)
        return smoothed.enumerated().map { PowerPt(x: $1.x, y: $1.y, gap: out[$0].gap) }
    }

    /// Energy drawn and recovered from the modelled power, and regen as a share of the draw (the "Regen recovered"
    /// tile).
    static func regen(_ p: [PowerPt]) -> (drawKwh: Double, regenKwh: Double, pct: Double) {
        var draw = 0.0, regen = 0.0
        if p.count > 1 {
            for i in 1..<p.count where !p[i].gap {
                let dtH = (p[i].x - p[i - 1].x) / 3_600_000
                let avg = (p[i].y + p[i - 1].y) / 2
                if avg >= 0 { draw += avg * dtH } else { regen += -avg * dtH }
            }
        }
        return (draw, regen, draw > 0.1 ? regen / draw * 100 : 0)
    }

    // MARK: Drive mode

    enum Mode: String, Equatable {
        case conserve, allPurpose, sport, other

        var label: String {
            switch self {
            case .conserve: return "Conserve"
            case .allPurpose: return "All-Purpose"
            case .sport: return "Sport"
            case .other: return "Other"
            }
        }
    }

    struct ModeSegment: Equatable {
        var x0: Double
        var x1: Double
        var mode: Mode
    }

    /// Rivian's wire values ("everyday", "sport", "distance") or display labels; without one, the gear.
    static func classifyMode(_ s: DriveSample) -> Mode {
        let raw = (s.driveMode ?? "").trimmingCharacters(in: .whitespaces).lowercased()
        if !raw.isEmpty {
            if raw == "sport" || raw.contains("launch") { return .sport }
            if raw == "distance" || raw.contains("conserve") || raw.contains("eco") { return .conserve }
            if raw == "everyday" || raw.contains("all purpose") || raw.contains("all-purpose") { return .allPurpose }
            return .other
        }
        let g = (s.shiftState ?? "").trimmingCharacters(in: .whitespaces).uppercased()
        return g == "D" || g == "DRIVE" ? .allPurpose : .other
    }

    /// Each sample's mode until the next sample, clipped to the drive; neighbours in the same mode (≤ 1.5 s apart)
    /// joined.
    static func modeSegments(_ s: [DriveSample], start: Double, end: Double) -> [ModeSegment] {
        var out: [ModeSegment] = []
        for (i, cur) in s.enumerated() {
            let curMs = ms(cur.at), nextMs = i + 1 < s.count ? ms(s[i + 1].at) : end
            guard nextMs > curMs else { continue }
            let seg = ModeSegment(x0: max(curMs, start), x1: min(nextMs, end), mode: classifyMode(cur))
            guard seg.x1 > seg.x0 else { continue }
            if let prev = out.last, prev.mode == seg.mode, abs(prev.x1 - seg.x0) <= 1500 {
                out[out.count - 1].x1 = seg.x1
            } else {
                out.append(seg)
            }
        }
        return out
    }

    // MARK: Moments

    struct Moment: Equatable, Identifiable {
        enum Kind: String { case highwayEntry, hardBrake, topSpeed, maxRegen, lowestSoC }
        enum Tone: Int { case neutral = 1, info, success, warning }
        let ms: Double
        let kind: Kind
        /// mph/s (hard brake), mph (top speed), kW (max regen, negative) or % (lowest SoC); 0 for highway entry.
        let value: Double
        let tone: Tone

        var id: String { "\(kind.rawValue)-\(ms)" }

        var label: String {
            switch kind {
            case .highwayEntry: return "Highway entry"
            case .hardBrake: return "Hard brake"
            case .topSpeed: return "Top speed"
            case .maxRegen: return "Max regen"
            case .lowestSoC: return "Lowest SoC"
            }
        }

        /// The web's detail line, in the phone's units.
        func detail(metric: Bool) -> String {
            let k = metric ? 1.609344 : 1
            switch kind {
            case .highwayEntry: return metric ? "Crossed 89 km/h and sustained for 30 s" : "Crossed 55 mph and sustained for 30 s"
            case .hardBrake: return "\(NumberText.fixed(value * k, 1)) \(metric ? "km/h" : "mph")/s deceleration"
            case .topSpeed: return "\(NumberText.fixed(value * k, 0)) \(metric ? "km/h" : "mph")"
            case .maxRegen: return "\(NumberText.fixed(value, 0)) kW recovered"
            case .lowestSoC: return "\(NumberText.fixed(value, 1)) %"
            }
        }
    }

    /// The web's moments (without the weather ones): Highway entry, Hard brake, Top speed, Max regen, Lowest SoC;
    /// two within 8 s keep the stronger tone.
    static func moments(speed: [Pt], soc: [Pt], power: [PowerPt]) -> [Moment] {
        var out: [Moment] = []
        // Highway entry: the first <40 → ≥55 mph crossing that holds ≥50 mph for 30 s.
        highway: for i in speed.indices.dropFirst() where speed[i - 1].y < 40 && speed[i].y >= 55 {
            for j in i..<speed.count {
                if speed[j].x - speed[i].x > 30_000 { break }
                if speed[j].y < 50 { continue highway }
            }
            out.append(Moment(ms: speed[i].x, kind: .highwayEntry, value: 0, tone: .info))
            break
        }
        // Hard brake: the steepest deceleration (≤ −5 mph/s), not the final stop.
        if let endMs = speed.last?.x {
            var worst: (ms: Double, rate: Double)?
            for i in speed.indices.dropFirst() {
                let dt = (speed[i].x - speed[i - 1].x) / 1000
                guard dt > 0, dt <= 10 else { continue }
                if speed[i].x > endMs - 30_000 && speed[i].y < 5 { continue }
                let rate = (speed[i].y - speed[i - 1].y) / dt
                if rate < -5 && rate < (worst?.rate ?? 0) { worst = (speed[i].x, rate) }
            }
            if let worst {
                out.append(Moment(ms: worst.ms, kind: .hardBrake, value: worst.rate, tone: .warning))
            }
        }
        if let best = speed.max(by: { $0.y < $1.y }), best.y >= 50 {
            out.append(Moment(ms: best.x, kind: .topSpeed, value: best.y, tone: .info))
        }
        if let best = power.min(by: { $0.y < $1.y }), best.y < -20 {
            out.append(Moment(ms: best.x, kind: .maxRegen, value: best.y, tone: .success))
        }
        // Lowest SoC: only when it isn't simply the end of the drive.
        if soc.count > 4, let end = soc.last {
            var bestIdx = 0
            for i in soc.indices.dropFirst() where soc[i].y < soc[bestIdx].y { bestIdx = i }
            let atEnd = soc[bestIdx].x >= end.x - 60_000 || abs(soc[bestIdx].y - end.y) < 0.05
            if bestIdx < soc.count - 1 && !atEnd {
                out.append(Moment(ms: soc[bestIdx].x, kind: .lowestSoC, value: soc[bestIdx].y, tone: .neutral))
            }
        }
        out.sort { $0.ms < $1.ms }
        var kept: [Moment] = []
        for m in out {
            if let prev = kept.last, m.ms - prev.ms < 8000 {
                if m.tone.rawValue > prev.tone.rawValue { kept[kept.count - 1] = m }
                continue
            }
            kept.append(m)
        }
        return kept
    }
}
