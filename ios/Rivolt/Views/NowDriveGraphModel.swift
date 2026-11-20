import Foundation

/// The numbers behind the Now card's driving graph (design_preview key now_drive_graph), as the web's DriveGraph works them out: the speed from the
/// start of the drive to now as one soft line (a smoothed summary, not every reading), the battery as a quieter second line, a quiet label at the
/// top speed (a real reading, not the smoothed curve), and the header's change since the first stored reading. Pure: no network, no views.
/// Points are in plot space: x across 0...1, y from the bottom 0...1.
struct NowDriveGraphModel: Equatable {
    typealias Pt = NowChargeGraphModel.Pt

    /// Under this much of the drive (90 s) or four points, the card is as it was: no frame, no "collecting".
    static let minSpan: TimeInterval = 90
    static let minPoints = 4
    /// How long into a drive the card says it just started (the minimum span and a minute and a half); after that, no readings.
    static let justStartedWindow: TimeInterval = minSpan + 90
    static func justStarted(startedAt: Date, now: Date) -> Bool { now.timeIntervalSince(startedAt) < justStartedWindow }
    /// At most this many readings per line, evenly spaced in time, so the curve through them has nothing to kink on.
    static let seriesPoints = 36

    let speed: [Pt]
    let soc: [Pt]
    /// The "now" dot.
    let last: Pt
    /// The top speed's label and where it sits, when it is a speed worth saying and not at the edge.
    let top: (pt: Pt, label: String)?
    /// The battery change, "−4%", when the truck has used some.
    let change: String?
    /// "since 11:38 AM".
    let since: String
    /// "11:38 AM", the axis label on a narrow card ("Left 11:38 AM" on a wide one).
    let startTime: String
    let speedUnit: String
    let spoken: String

    static func == (a: Self, b: Self) -> Bool {
        a.speed == b.speed && a.soc == b.soc && a.last == b.last && a.top?.pt == b.top?.pt && a.top?.label == b.top?.label
            && a.change == b.change && a.since == b.since && a.startTime == b.startTime && a.speedUnit == b.speedUnit
    }

    /// The window of smoothing: a thirtieth of the drive, 12 s to a minute, so a long drive is drawn as its shape and not as every reading's jitter.
    static func sigmaMs(span: Double) -> Double { min(max(span / 30, 12_000), 60_000) }

    /// At most `seriesPoints` points, evenly spaced in time and read off the line (the last one exactly); a short series is kept as it is.
    static func evenly(_ pts: [DriveTimelineMath.Pt]) -> [DriveTimelineMath.Pt] {
        guard pts.count > seriesPoints, let first = pts.first, let last = pts.last else { return pts }
        var out: [DriveTimelineMath.Pt] = []
        var j = 0
        for i in 0..<seriesPoints {
            let x = i == seriesPoints - 1 ? last.x : first.x + (last.x - first.x) * Double(i) / Double(seriesPoints - 1)
            while j < pts.count - 2 && pts[j + 1].x < x { j += 1 }
            let a = pts[j], b = pts[j + 1]
            out.append(.init(x: x, y: b.x == a.x ? b.y : a.y + (b.y - a.y) * min(max((x - a.x) / (b.x - a.x), 0), 1)))
        }
        return out
    }

    static func make(samples: [DriveSample], startedAt: Date, nowPct: Double?, metric: Bool, time: (Date) -> String = { $0.formatted(date: .omitted, time: .shortened) }) -> NowDriveGraphModel? {
        let from = startedAt.addingTimeInterval(-5)
        let mine = samples.filter { $0.at >= from }.sorted { $0.at < $1.at }
        let raw = mine.map { DriveTimelineMath.Pt(x: DriveTimelineMath.ms($0.at), y: max($0.speedMph ?? 0, 0)) }
        guard let r0 = raw.first, let r1 = raw.last else { return nil }
        let span = raw.count > 1 ? r1.x - r0.x : 0
        let speed = evenly(DriveTimelineMath.smoothGaussianTime(raw, sigmaMs: sigmaMs(span: span)))
        let batt = mine.compactMap { p in p.batteryLevelPct.flatMap { $0 > 0 ? DriveTimelineMath.Pt(x: DriveTimelineMath.ms(p.at), y: $0) : nil } }
        let soc = evenly(DriveTimelineMath.smoothGaussianTime(batt, sigmaMs: 30_000))
        guard let first = speed.first, let last = speed.last, speed.count >= minPoints, last.x - first.x >= minSpan * 1000 else { return nil }
        let k = metric ? 1.609344 : 1
        let unit = metric ? "km/h" : "mph"
        let realTop = raw.reduce(raw[0]) { $1.y > $0.y ? $1 : $0 }
        let peak = max(speed.map { $0.y * k }.max() ?? 0, realTop.y * k, 10)
        let ceiling = (peak * 1.15 / 10).rounded(.up) * 10
        let sx = { (x: Double) in (x - first.x) / (last.x - first.x) }
        let sv = { (v: Double) in v / ceiling }
        let socHi = max(soc.map(\.y).max() ?? 1, 1)
        let socLo = min(soc.map(\.y).min() ?? socHi, socHi)
        let socSpan = max(socHi - socLo, 3)
        let ss = { (v: Double) in (v - (socLo - socSpan * 0.1)) / (socSpan * 1.2) }
        // The change is the card's own battery figure (the ring) against the first stored reading, as the charging graph does.
        let used: Int? = {
            guard let nowPct, nowPct > 0, let first = batt.first else { return nil }
            let u = Int(first.y.rounded()) - Int(nowPct.rounded())
            return u > 0 ? u : nil
        }()
        let topShown = realTop.y * k >= 10 && realTop.x >= first.x && realTop.x < last.x - 60_000
        // The drive's own start (the same one the axis label says), not the first stored reading.
        let sinceText = "since \(time(startedAt))"
        let nowSpeed = Int((last.y * k).rounded())
        let spoken = "Speed \(sinceText), \(nowSpeed) \(unit) now, top \(Int(peak.rounded())) \(unit)" + (used.map { ", battery down \($0) percent" } ?? "")
        return NowDriveGraphModel(
            speed: speed.map { Pt(x: sx($0.x), y: sv($0.y * k)) },
            soc: soc.count > 1 ? soc.map { Pt(x: sx($0.x), y: ss($0.y)) } : [],
            last: Pt(x: 1, y: sv(last.y * k)),
            top: topShown ? (Pt(x: sx(realTop.x), y: sv(realTop.y * k)), "\(Int((realTop.y * k).rounded())) \(unit)") : nil,
            change: used.map { "−\($0)%" },
            since: sinceText,
            startTime: time(startedAt),
            speedUnit: unit,
            spoken: spoken)
    }

    /// "12 mi to Work · arrive 12:05 PM" (no distance when unknown), the web's `destinationShort`.
    static func destinationLine(_ d: LiveDestination, metric: Bool = NowFormat.usesMetric, arrive: (Date) -> String = { $0.formatted(date: .omitted, time: .shortened) }) -> String {
        let f = d.figures(metric: metric, arrive: arrive)
        let head = f.left.map { "\($0) to \(d.name)" } ?? "To \(d.name)"
        return "\(head) · arrive \(f.arrive)"
    }
}
