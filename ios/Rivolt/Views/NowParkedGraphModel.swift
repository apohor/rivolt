import Foundation

/// The numbers and words behind the Now card's "Battery while parked" graph (design_preview key now_parked_graph), as the web's ParkedGraph and
/// parkedSeries.ts work them out: the last 24 hours of the battery as one soft line (a drive or charge between readings is bridged, not split),
/// each sleep as a soft fill under the line with its length, a dot at each wake-up, and the start and now readings. The line ends at the card's own
/// battery (the ring) and starts at the first stored reading. The header is the difference of the two labels: with a drive or charge in the window
/// the start label sits on the reading at the parked marker and the header says "since <chip time>"; parked the whole window it says "in 24 h".
/// Pure: no network, no views. Points are in plot space: x across 0...1, y from the bottom 0...1.
struct NowParkedGraphModel: Equatable {
    struct Pt: Equatable {
        let x: Double
        let y: Double
    }

    struct Sleep: Equatable {
        let x0: Double
        let x1: Double
        /// The band's label, "6 h 10" or "45 min".
        let label: String
    }

    struct Figure: Equatable {
        enum ID: String { case asleep, wakes, idle }
        let id: ID
        let value: String
        /// Under the value on the desktop row: "≈ 61 mi of range"; empty when there is none.
        let note: String
        /// Under the value on a phone tile, which has no room for "of range"; falls back to `label`.
        let tileNote: String
        let label: String
        let rowLabel: String
        let rowValue: String
    }

    let line: [Pt]
    let sleeps: [Sleep]
    let wakes: [Pt]
    /// The parked marker's x, when the parking began inside the window.
    let parkedX: Double?
    let parkedLabel: String?
    let start: (pt: Pt, pct: Double)
    let last: (pt: Pt, pct: Double)
    /// The "−1.7%" and "since 6:20 AM" (or "in 24 h") of the header.
    let change: String
    let since: String
    /// The reading's own time when the header measures from the parked marker.
    let sinceAt: Date?
    let figures: [Figure]
    let projection: String?
    let sleepCount: Int
    let wakeCount: Int

    static func == (a: Self, b: Self) -> Bool {
        a.line == b.line && a.sleeps == b.sleeps && a.wakes == b.wakes && a.parkedX == b.parkedX && a.parkedLabel == b.parkedLabel
            && a.start.pt == b.start.pt && a.start.pct == b.start.pct && a.last.pt == b.last.pt && a.last.pct == b.last.pct
            && a.change == b.change && a.since == b.since && a.sinceAt == b.sinceAt && a.figures == b.figures && a.projection == b.projection
            && a.sleepCount == b.sleepCount && a.wakeCount == b.wakeCount
    }

    /// The graph draws only when there is a line to draw: a few readings over the window.
    static func drawable(_ s: ParkedSeries?) -> Bool { (s?.points.count ?? 0) >= 4 }

    // MARK: Words (the web's parkedSeries.ts)

    /// "18 h 40 min", "45 min": the figure under the graph.
    static func longDuration(_ sec: Double) -> String {
        let m = Int(max(0, sec / 60).rounded())
        return m < 60 ? "\(m) min" : "\(m / 60) h \(String(format: "%02d", m % 60)) min"
    }

    /// "6 h 10", "45 min": the label inside a sleep band.
    static func bandDuration(_ sec: Double) -> String {
        let m = Int(max(0, sec / 60).rounded())
        return m < 60 ? "\(m) min" : "\(m / 60) h \(String(format: "%02d", m % 60))"
    }

    /// The header change: the loss since the truck was parked. The base is the reading at the parked marker, so a drive or a charge before it never
    /// counts; when the parking began before the window the base is the window's first reading and the header says "in 24 h".
    static func parkedChange(base: Double, now: Double, since: Date?) -> (value: String, since: String) {
        let d = (now - base) * 10
        let r = d.rounded() / 10
        let sign = r < 0 ? "−" : "+"
        let value = r == 0 ? "No loss" : "\(sign)\(NumberText.fixed(abs(r), 1))%"
        return (value, since.map { "since \(clock($0))" } ?? "in 24 h")
    }

    /// The start of the sleep an asleep truck is in, from the series: a time; `.some(nil)` when that sleep began before the 24 h window (its start is
    /// not known); `nil` when the series has no open sleep.
    static func openSleepSince(_ s: ParkedSeries) -> Date?? {
        guard let sl = s.sleeps.last, sl.open else { return nil }
        return sl.from <= s.from ? .some(nil) : .some(sl.from)
    }

    /// "Parked 5:20 PM": the chip's own time, so the card says one parked time.
    static func parkedAtLabel(_ at: Date) -> String { "Parked \(clock(at))" }

    static func clock(_ d: Date) -> String { d.formatted(date: .omitted, time: .shortened) }

    /// The tiles and rows. `metric` picks km over mi for the idle loss's range.
    static func figures(_ s: ParkedSeries, metric: Bool) -> [Figure] {
        guard let f = s.figures else { return [] }
        var out = [Figure(id: .asleep, value: longDuration(f.asleepTodayS), note: "", tileNote: "", label: "asleep today", rowLabel: "Asleep today",
                          rowValue: longDuration(f.asleepTodayS))]
        if f.wakeUps > 0 {
            // A cost that rounds to 0.0% says nothing: the count stands alone.
            let note = f.wakeCostPct.flatMap { $0 >= 0.05 ? "≈ \(NumberText.fixed($0, 1))% each" : nil } ?? ""
            out.append(Figure(id: .wakes, value: "\(f.wakeUps) \(f.wakeUps == 1 ? "wake-up" : "wake-ups")", note: note, tileNote: note, label: "wake-ups",
                              rowLabel: "Wake-ups", rowValue: String(f.wakeUps)))
        }
        if let pct = f.idlePctPerDay {
            let dist = f.idleMiPerDay.map { Int(($0 * (metric ? 1.609344 : 1)).rounded()) }
            let unit = metric ? "km" : "mi"
            let value = "\(Int(pct.rounded()))% a day"
            out.append(Figure(id: .idle, value: value, note: dist.map { "≈ \($0) \(unit) of range" } ?? "", tileNote: dist.map { "≈ \($0) \(unit)" } ?? "",
                              label: "idle loss", rowLabel: "Idle loss", rowValue: value))
        }
        return out
    }

    /// "At this rate: about 61% by Monday 8:00 AM (estimate)."; nil until the truck has been parked a few hours.
    static func projectionText(_ s: ParkedSeries) -> String? {
        guard let p = s.projection else { return nil }
        return "At this rate: about \(Int(p.soc.rounded()))% by \(p.at.formatted(.dateTime.weekday(.wide))) \(clock(p.at)) (estimate)."
    }

    // MARK: Plot

    /// Nil while there is no line to draw. `nowPct` is the ring's value: the line ends there instead of at the last stored reading.
    static func make(series s: ParkedSeries, nowPct: Double?, metric: Bool) -> NowParkedGraphModel? {
        guard drawable(s), let lastStored = s.points.last else { return nil }
        let t0 = s.from.timeIntervalSince1970
        let t1 = s.to.timeIntervalSince1970
        guard t1 > t0 else { return nil }
        var pts = s.points
        if let nowPct, nowPct > 0 {
            pts = pts.filter { s.to.timeIntervalSince($0.t) > 60 } + [.init(t: s.to, soc: nowPct, seg: lastStored.seg)]
        }
        guard let first = pts.first, let last = pts.last else { return nil }
        let hi = pts.map(\.soc).max() ?? last.soc
        let lo = pts.map(\.soc).min() ?? last.soc
        let span = max(hi - lo, 4)
        // The line rides the upper half so the sleep spans and their labels sit under it.
        let yHi = hi + span * 0.15
        let yLo = lo - span * 1.1
        let sx = { (d: Date) in (d.timeIntervalSince1970 - t0) / (t1 - t0) }
        let sy = { (v: Double) in (v - yLo) / (yHi - yLo) }
        let parkedAt = s.parkedSince
        let parkedX = parkedAt.flatMap { $0 > s.from && $0 < s.to ? sx($0) : nil }
        // Only readings from the parked marker on count toward the change: a drive or a charge earlier in the window is not "while parked".
        let sinceIn = parkedAt.flatMap { $0 > s.from ? $0 : nil }
        let curSeg = pts.filter { $0.seg == last.seg }
        let base = sinceIn != nil ? (curSeg.first ?? first) : first
        let c = parkedChange(base: base.soc, now: last.soc, since: sinceIn)
        let sleeps: [Sleep] = s.sleeps.compactMap { sl in
            let a = max(sl.from, s.from)
            let b = min(sl.to, s.to)
            guard b > a else { return nil }
            return Sleep(x0: sx(a), x1: sx(b), label: "☾ \(bandDuration(b.timeIntervalSince(a)))")
        }
        return NowParkedGraphModel(
            line: pts.map { Pt(x: sx($0.t), y: sy($0.soc)) },
            sleeps: sleeps,
            wakes: s.wakes.map { Pt(x: sx($0.t), y: sy($0.socAfter)) },
            parkedX: parkedX,
            parkedLabel: parkedAt.map(parkedAtLabel),
            start: (Pt(x: sx(base.t), y: sy(base.soc)), base.soc),
            last: (Pt(x: sx(last.t), y: sy(last.soc)), last.soc),
            change: c.value, since: c.since, sinceAt: sinceIn,
            figures: figures(s, metric: metric), projection: projectionText(s),
            sleepCount: s.sleeps.count, wakeCount: s.wakes.count)
    }

    /// For VoiceOver.
    var spoken: String {
        let from = sinceAt.map { "at \(Self.clock($0))" } ?? "24 hours ago"
        return "Battery while parked, \(NumberText.fixed(start.pct, 0)) percent \(from) to \(NumberText.fixed(last.pct, 0)) percent now, "
            + "\(change.replacingOccurrences(of: "−", with: "minus ")) \(since), \(sleepCount) \(sleepCount == 1 ? "sleep" : "sleeps"), "
            + "\(wakeCount) \(wakeCount == 1 ? "wake-up" : "wake-ups")."
    }
}
