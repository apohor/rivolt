import Foundation

/// The web's Truck rules (lib/drain.ts, lib/truckHealth.ts, lib/lease.ts, lib/minutes.ts and the Truck cards), kept
/// out of the views so they can be tested. Each card says what happened and how much data it rests on; when the data
/// is thin it says that instead of a number, and it never names a cause.
enum TruckLogic {
    static func fixed(_ v: Double, _ digits: Int) -> String { NumberText.fixed(v, digits) }
    static func plural(_ n: Int, _ one: String, _ many: String) -> String { n == 1 ? one : many }
    static func whole(_ v: Double) -> String { Int(v.rounded()).formatted() }

    /// "Excludes 3 drives with a trailer, which ran 1.40 mi/kWh over 120 mi.", the web's words; nil when none were left out.
    static func trailerTrendNote(_ t: TruckTrends) -> String? {
        let n = t.excludedTrailerDrives ?? 0
        guard n > 0 else { return nil }
        var s = "Excludes \(n) \(plural(n, "drive", "drives")) with a trailer"
        let rows = t.efficiency.compactMap(\.trailer)
        let miles = rows.reduce(0) { $0 + $1.distance }, kwh = rows.reduce(0) { $0 + $1.energyKwh }
        if kwh > 0 { s += ", which ran \(fixed(miles / kwh, 2)) \(t.efficiencyUnit) over \(whole(miles)) \(t.distanceUnit)" }
        return s + "."
    }

    // MARK: Parked drain

    /// "0.6" under 10, "12" above.
    static func pct(_ v: Double) -> String { v < 10 ? fixed(v, 1) : fixed(v, 0) }

    /// "45 min", "3.4 h", "12 h".
    static func hours(_ h: Double) -> String {
        if h < 1 { return "\(Int((h * 60).rounded())) min" }
        return "\(h < 10 ? fixed(h, 1) : String(Int(h.rounded()))) h"
    }

    enum DrainKey: String, CaseIterable, Identifiable {
        case week = "7d", month = "30d"
        var id: String { rawValue }
        var label: String { self == .week ? "7 days" : "30 days" }
        var other: DrainKey { self == .week ? .month : .week }
        /// What `sufficient` needs server-side, so the card can say how far along the truck is.
        var neededHours: Int { self == .week ? 24 : 72 }
        var span: String { self == .week ? "this week" : "over 30 days" }
    }

    static func window(_ d: DrainSnapshot, _ k: DrainKey) -> DrainWindow { k == .week ? d.windows.week : d.windows.month }

    enum DrainView: Equatable {
        case insufficient(haveHours: Double, needHours: Int)
        case ok(pctPerDay: Double, kwhPerDay: Double?, sleeps: Int, quickRewakes: Int, medianSleepSeconds: Double?, daysUsed: Int, daysExcluded: Int)
    }

    /// Thin data (or a missing rate) never yields a number.
    static func drainView(_ w: DrainWindow, _ k: DrainKey) -> DrainView {
        guard w.sufficient, let p = w.drainPctPerDay else { return .insufficient(haveHours: w.parkedHours, needHours: k.neededHours) }
        let ex = w.daysExcluded
        return .ok(pctPerDay: p, kwhPerDay: w.drainKwhPerDay, sleeps: w.sleeps, quickRewakes: w.quickRewakes, medianSleepSeconds: w.medianSleepSeconds,
                   daysUsed: w.daysWithData, daysExcluded: (ex?.insufficient ?? 0) + (ex?.charging ?? 0) + (ex?.driving ?? 0))
    }

    /// "Over 7 days: 22 sleeps, median 3.4 h. Time spent awake while parked is in the Sleep card below."
    static func drainSleeps(_ k: DrainKey, sleeps: Int, quickRewakes: Int, medianSleepSeconds: Double?) -> String {
        var s = "Over \(k == .week ? "7" : "30") days: \(sleeps) \(plural(sleeps, "sleep", "sleeps"))"
        if quickRewakes > 0 { s += ", \(quickRewakes) under a minute" }
        if let m = medianSleepSeconds { s += ", median \(hours(m / 3600))" }
        return s + ". Time spent awake while parked is in the Sleep card below."
    }

    /// "Based on 7 days (3 left out: too little parked data). 30 days: 1.5% / day."
    static func drainBasis(daysUsed: Int, daysExcluded: Int, other: DrainKey, otherView: DrainView) -> String {
        var s = "Based on \(daysUsed) \(plural(daysUsed, "day", "days"))"
        if daysExcluded > 0 { s += " (\(daysExcluded) left out: too little parked data)" }
        s += "."
        if case let .ok(p, _, _, _, _, _, _) = otherView { s += " \(other.label): \(pct(p))% / day." }
        return s
    }

    struct DrainBar: Equatable, Identifiable {
        let day: String
        /// Nil for a day that is left out or unmeasured (drawn outlined).
        let value: Double?
        let excluded: String?
        /// 0...1 against the series' own maximum.
        let height: Double
        /// The battery rose while parked (noise or a top-up): drawn faint, never as a fake 0%.
        let gained: Double?
        var id: String { day }
    }

    static func drainBars(_ daily: [DrainDay]) -> (bars: [DrainBar], max: Double) {
        let measured = daily.map { $0.excluded == nil ? $0.drainPct : nil }
        let vals = measured.map { $0.map { max(0, $0) } }
        let top = max(0.5, vals.compactMap { $0 }.max() ?? 0)
        let bars = zip(daily, zip(measured, vals)).map { d, mv in
            DrainBar(day: d.day, value: mv.1, excluded: d.excluded, height: (mv.1 ?? 0) / top,
                     gained: mv.0.flatMap { $0 < 0 ? -$0 : nil })
        }
        return (bars, top)
    }

    enum SplitView: Equatable {
        case sparse, gaps
        /// The sentence, the awake share (0...1) and which parts have any loss.
        case ok(text: String, awakeShare: Double, awake: Bool, asleep: Bool)
    }

    /// The selected window's split, unless it is too thin and the other one has enough: then that one, said as such.
    static func split(_ d: DrainSnapshot, _ k: DrainKey) -> SplitView? {
        let mine = window(d, k).split
        let theirs = window(d, k.other).split
        let useOther = mine.map { !$0.sufficient } == true && theirs?.sufficient == true
        guard let s = useOther ? theirs : mine else { return nil }
        let span = (useOther ? k.other : k).span
        guard s.sufficient else {
            switch s.reason {
            case "not_enough_data": return .sparse
            case "power_state_gaps": return .gaps
            default: return nil
            }
        }
        let asleep = s.asleepPointsLost ?? 0
        let awake = s.awakePointsLost ?? 0
        let total = asleep + awake
        guard total > 0 else { return nil }
        var text = "Of the \(Int(total.rounded()))% your truck lost while parked \(span), about \(Int(awake.rounded()))% was while it was awake"
        if let h = s.awakeHours { text += " (\(hours(h)))" }
        return .ok(text: text + ".", awakeShare: s.awakeShareOfLoss ?? 0, awake: awake > 0, asleep: asleep > 0)
    }

    static let excludedLabel = ["charging": "charging most of the day", "driving": "mostly driving", "insufficient": "not enough data"]

    // MARK: Battery capacity

    /// Our spec table has one pack (Max) at a gross 180 kWh; only a usable figure is shown next to the truck's own.
    static func specKwh(_ kwh: Double?) -> Double {
        guard let kwh, kwh > 0, kwh <= 150 else { return 0 }
        return kwh
    }

    enum PackView: Equatable {
        /// The truck's own usable capacity, with Rivian's spec when it is a usable figure.
        case reported(kwh: Double, spec: Double)
        /// Our estimate from charges.
        case estimate(kwh: Double?, spec: Double)
        case none
    }

    /// The one line at the top of Truck (the scroll audit's "health headline"): the pack's usable capacity and the 7-day
    /// parked drain, each exactly as its card below shows it (the same helpers, rounding and words), dropped when its card
    /// has no figure; nil when neither has. Plain figures: no score, colour, comparison with the spec or "degradation".
    static func summary(pack: PackHealth?, trends: TruckTrends? = nil, drain: DrainSnapshot?) -> String? {
        let parts = summaryParts(pack: pack, trends: trends, drain: drain)
        guard !parts.isEmpty else { return nil }
        return parts.enumerated().map { $0.offset == 0 ? $0.element.text : $0.element.text.prefix(1).lowercased() + $0.element.text.dropFirst() }
            .joined(separator: " · ")
    }

    /// The line's halves, each capitalised (a row of its own at large text) and naming the card it summarises ("pack",
    /// "drain"), so a tap can show that card.
    struct SummaryPart: Equatable, Identifiable {
        let id: String
        let text: String
    }

    static func summaryParts(pack: PackHealth?, trends: TruckTrends? = nil, drain: DrainSnapshot?) -> [SummaryPart] {
        var parts: [SummaryPart] = []
        // The Battery capacity card's headline: the truck's own figure, else our estimate (the trends median).
        switch battery(pack: pack, trends: trends).head {
        case let .reported(kwh)?: parts.append(SummaryPart(id: "pack", text: "Pack \(fixed(kwh, 1)) kWh usable, as your truck reports it"))
        case let .estimate(kwh)?: parts.append(SummaryPart(id: "pack", text: "Pack about \(fixed(kwh, 0)) kWh usable (estimate)"))
        case nil: break
        }
        if let drain, case let .ok(p, _, _, _, _, _, _) = drainView(window(drain, .week), .week) {
            parts.append(SummaryPart(id: "drain", text: "Parked drain \(pct(p))% a day (last 7 days)"))
        }
        return parts
    }

    static func pack(_ p: PackHealth) -> (head: PackView, trend: [PackHealth.Sample], note: String) {
        let samples = p.samples ?? []
        let h = p.headline
        let spec = specKwh(h.documentedKwh)
        let reported = h.reportedKwh ?? 0
        if samples.isEmpty {
            let head: PackView = reported > 0 ? .reported(kwh: reported, spec: spec) : .none
            return (head, [], reported > 0
                ? "Effective-capacity trend needs a charge session spanning 30%+ SoC."
                : "Not enough qualifying charge sessions yet. We need at least one session that spans 30%+ SoC to estimate effective pack capacity.")
        }
        let clean = samples.filter { !$0.derateActive }
        let head: PackView = reported > 0 ? .reported(kwh: reported, spec: spec) : .estimate(kwh: h.effectiveKwh > 0 ? h.effectiveKwh : nil, spec: spec)
        return (head, clean.isEmpty ? samples : clean,
                "Median of the last \(h.window) clean session\(h.window == 1 ? "" : "s") · \(h.sampleCount) total")
    }

    // MARK: Trends

    /// "Oct 26", the web's month label.
    static func monthLabel(_ ym: String) -> String {
        guard let d = JSON.parseDate("\(ym)-15T12:00:00Z") else { return ym }
        var f = Date.FormatStyle(timeZone: TimeZone(identifier: "UTC")!).month(.abbreviated).year(.twoDigits)
        f.calendar = Calendar(identifier: .gregorian)
        return d.formatted(f)
    }

    enum CapacityView: Equatable {
        case untracked
        case sparse(String)
        case ok(median: Double, spec: Double, need: Double)
    }

    /// The one "Battery capacity" card (the owner: two numbers in two places confused). The headline is what the truck
    /// reports; without one, our estimate from charges (the trends median, the history the dots show). The other figure
    /// goes on one labelled line. One estimate only: the pack-health "median of clean sessions" no longer shows. The web's
    /// lib/batteryCapacity.ts says the same.
    struct BatteryCapacity: Equatable {
        enum Head: Equatable {
            case reported(kwh: Double)
            case estimate(kwh: Double)
        }
        var head: Head?
        /// Our estimate, when the server's rule for it is met.
        var estimateKwh: Double?
        var charges: Int = 0
        /// Rivian's spec when it is a usable figure, else 0.
        var spec: Double = 0
        var progress: String?
        var untracked = false
        var need: Double = 30

        /// The other figure under the truck's own: our estimate, labelled as ours.
        var estimateLine: String? {
            guard case .reported = head, let e = estimateKwh else { return nil }
            return "Our estimate from your charges: about \(TruckLogic.fixed(e, 0)) kWh, from \(charges) \(charges == 1 ? "charge" : "charges")."
        }
    }

    static func battery(pack: PackHealth?, trends: TruckTrends?) -> BatteryCapacity {
        var b = BatteryCapacity()
        let reported = pack?.headline.reportedKwh ?? 0
        if let t = trends {
            switch capacity(t) {
            case .untracked: b.untracked = true
            case .sparse(let s): b.progress = s
            case let .ok(median, _, need):
                b.estimateKwh = median
                b.charges = t.capacity?.samples.count ?? 0
                b.need = need
            }
        }
        b.spec = specKwh(pack?.headline.documentedKwh ?? trends?.capacity?.specKwh)
        if reported > 0 { b.head = .reported(kwh: reported) } else if let e = b.estimateKwh { b.head = .estimate(kwh: e) }
        return b
    }

    static func capacity(_ t: TruckTrends) -> CapacityView {
        guard let c = t.capacity else { return .untracked }
        let need = c.minSocDeltaPct ?? 30
        guard let median = c.medianKwh, c.samples.count >= 2 else {
            var s = "Our estimate needs about \(c.minSamples) charges that add at least \(whole(need))% of battery, over at least \(whole(c.minSpanDays)) days. So far: \(c.qualifyingCount) of \(c.minSamples) charges"
            if c.spanDays < c.minSpanDays { s += ", over \(whole(c.spanDays)) of \(whole(c.minSpanDays)) days" }
            return .sparse(s + ".")
        }
        return .ok(median: median, spec: specKwh(c.specKwh), need: need)
    }

    // MARK: Range at different temperatures

    /// The five temperatures offered, in °C (the server works in °C): 0, 20, 40, 60, 80 °F, or round °C steps.
    static func grid(fahrenheit: Bool) -> [Double] { fahrenheit ? [-17.8, -6.7, 4.4, 15.6, 26.7] : [-20, -10, 0, 10, 20] }

    static func near(_ a: Double, _ b: Double) -> Bool { abs(a - b) < 0.3 }

    /// A chip's label: whole degrees, never "-0".
    static func chip(_ c: Double, fahrenheit: Bool) -> String {
        let v = Int((fahrenheit ? c * 1.8 + 32 : c).rounded())
        return "\(v == 0 ? 0 : v) °\(fahrenheit ? "F" : "C")"
    }

    static func temperature(_ c: Double, fahrenheit: Bool) -> String {
        "\(fixed(fahrenheit ? c * 1.8 + 32 : c, 0)) °\(fahrenheit ? "F" : "C")"
    }

    /// Greyed out: a chip with too little history of the owner's own.
    static func chipOff(_ d: ColdRange, _ t: Double) -> Bool {
        (!d.available.isEmpty || d.reason == "not_enough_data") && !d.available.contains { near($0, t) }
    }

    /// One value when the band's ends round alike, never "283–283".
    static func band(_ lo: Double, _ hi: Double, digits: Int, unit: String) -> String {
        let a = fixed(lo, digits), b = fixed(hi, digits)
        return a == b ? "\(a) \(unit)" : "\(a)–\(b) \(unit)"
    }

    // MARK: Lease

    enum LeaseTone: Equatable { case ok, watch, over }

    struct LeaseBar: Equatable {
        let used: Double
        let plan: Double
        let allowed: Double
        let projected: Double?
        let tone: LeaseTone
    }

    /// Positions 0...1 on a track as wide as the larger of the allowance, the projection and the miles used.
    static func leaseBar(_ st: LeaseStatus, allowedTotal: Double) -> LeaseBar {
        let used = st.usedMi ?? 0
        let scale = max(allowedTotal, st.projectedTotalMi ?? 0, used, 1)
        func f(_ v: Double) -> Double { max(0, min(1, v / scale)) }
        let tone: LeaseTone = used > allowedTotal ? .over : used > st.allowedToDateMi ? .watch : .ok
        return LeaseBar(used: f(used), plan: f(st.allowedToDateMi), allowed: f(allowedTotal), projected: st.projectedTotalMi.map(f), tone: tone)
    }

    /// "1,720 mi under plan", "340 mi over plan" or "on plan", in the phone's unit.
    static func planStanding(_ st: LeaseStatus, metric: Bool) -> (text: String, tone: LeaseTone)? {
        guard let ab = st.aheadBehindMi else { return nil }
        let d = Int(abs(ab).rounded())
        if d == 0 { return ("on plan", .ok) }
        let n = distance(Double(d), metric: metric)
        return ab > 0 ? ("\(n) under plan", .ok) : ("\(n) over plan", .watch)
    }

    /// Lease miles in the phone's unit: "36,000 mi" or "57,936 km".
    static func distance(_ mi: Double, metric: Bool) -> String {
        "\(whole(metric ? mi * 1.609344 : mi)) \(metric ? "km" : "mi")"
    }

    // MARK: Sleep and idle-awake

    /// The web's sleep window for a period: UTC days, at most the last 90, through today.
    static func sleepWindow(_ month: PeriodMonth, now: Date, calendar: Calendar) -> (since: Date, until: Date, capped: Bool) {
        let day: TimeInterval = 86_400
        func utcDay(_ t: TimeInterval) -> TimeInterval { (t / day).rounded(.down) * day }
        let r = month.range(calendar: calendar)
        let end = min(r.until, now).timeIntervalSince1970
        let wanted = r.since.timeIntervalSince1970
        let floor = end - 90 * day
        return (Date(timeIntervalSince1970: utcDay(max(wanted, floor))), Date(timeIntervalSince1970: utcDay(end) + day), wanted < floor)
    }

    struct SleepSummary: Equatable {
        let bars: [DayValue]
        /// Failed sleeps across the last two days, when it's worth saying so.
        let trouble: (failed: Int, sleeps: Int)?
        let average: Double?
        let averageDays: Int

        static func == (a: SleepSummary, b: SleepSummary) -> Bool {
            a.bars == b.bars && a.trouble?.failed == b.trouble?.failed && a.trouble?.sleeps == b.trouble?.sleeps && a.average == b.average && a.averageDays == b.averageDays
        }
    }

    static func sleepSummary(_ days: [DayActivity], now: Date, calendar: Calendar, threshold: Int = 3) -> SleepSummary {
        let bars = ChartMath.idleMinutes(days, calendar: calendar)
        let recent = days.suffix(2)
        let failed = recent.reduce(0) { $0 + ($1.failedSleeps ?? 0) }
        let sleeps = recent.reduce(0) { $0 + ($1.sleeps ?? 0) }
        let today = calendar.startOfDay(for: now)
        let done = bars.filter { $0.day < today }
        return SleepSummary(bars: bars, trouble: failed >= threshold ? (failed, sleeps) : nil, average: ChartMath.averageIdle(bars, now: now, calendar: calendar),
                            averageDays: done.count)
    }

    /// "2 h 10 min", "45 min", "3 h".
    static func minutes(_ m: Double) -> String {
        let t = max(0, Int(m.rounded())), h = t / 60, r = t % 60
        if h == 0 { return "\(r) min" }
        return r == 0 ? "\(h) h" : "\(h) h \(r) min"
    }

    /// The axis form: "45m", "2h 10m".
    static func minutesShort(_ m: Double) -> String {
        let t = max(0, Int(m.rounded())), h = t / 60, r = t % 60
        if h == 0 { return "\(r)m" }
        return r == 0 ? "\(h)h" : "\(h)h \(r)m"
    }

    // MARK: Vehicle

    static let lowTireBar = 2.2

    /// The lowest wheel that reported; no wheel position is claimed.
    static func lowestTire(_ s: VehicleState) -> (bar: Double, psi: Double, low: Bool)? {
        let vals = [s.tirePressureFlBar, s.tirePressureFrBar, s.tirePressureRlBar, s.tirePressureRrBar].compactMap { $0 }.filter { $0.isFinite && $0 > 0 }
        guard let bar = vals.min() else { return nil }
        return (bar, bar * 14.5038, bar < lowTireBar)
    }

    /// Sleep health in words, the web's (`sleepHealthLines`): the verdict, what a short sleep is with this week's numbers, and what it is
    /// compared with. Neutral on purpose: it never says why the truck woke and never says the truck is fine.
    struct SleepHealthLines: Equatable {
        let headline: String
        let detail: String?
        let basis: String
    }

    static func sleepHealthLines(_ h: SleepHealth) -> SleepHealthLines {
        let basis = "Compared with this truck's own last 30 days."
        if h.state == .notEnoughData { return SleepHealthLines(headline: "Not enough history yet to say how this week compares.", detail: nil, basis: basis) }
        let n = h.thisWeek.failedSleeps
        let usual = h.usualFailedSleeps ?? 0
        let usualText = usual < 0.5 ? "almost none in a usual week" : "about \(Int(usual.rounded())) in a usual week"
        let detail = "\(n) short \(n == 1 ? "sleep" : "sleeps") (under a minute) this week; \(usualText)."
        if h.state == .more {
            let by = h.moreBy ?? 0
            return SleepHealthLines(headline: "Woke \(by) more \(by == 1 ? "time" : "times") than usual this week.", detail: detail, basis: basis)
        }
        return SleepHealthLines(headline: "Nothing unusual this week.", detail: detail, basis: basis)
    }

    struct Wheel: Equatable, Identifiable {
        let label: String
        let bar: Double?
        let psi: Double?
        /// Below the fixed floor (`lowTireBar`).
        let low: Bool
        /// Rivian's own status for this wheel is one of its low words.
        let rivianLow: Bool
        var id: String { label }
    }

    /// The words Rivian's per-wheel status uses for a low tire, after trimming, lower-casing and turning spaces and hyphens into "_".
    /// Any other word (including "normal") makes no claim: the card shows the pressure only and never calls a wheel OK in our words.
    private static let rivianLowWords: Set<String> = ["low", "low_pressure", "very_low", "critically_low", "critical_low", "critical"]

    static func rivianSaysLow(_ status: String?) -> Bool {
        let key = (status ?? "").trimmingCharacters(in: .whitespaces).lowercased().split(whereSeparator: { $0 == " " || $0 == "-" || $0 == "_" }).joined(separator: "_")
        return rivianLowWords.contains(key)
    }

    /// The four wheels, front row first as on the truck. A wheel that sent no pressure (0) has no number and is never marked low on the
    /// floor; it can still carry Rivian's own word. Nil when no wheel has a pressure to show.
    static func wheels(_ s: VehicleState) -> [Wheel]? {
        let rows: [(String, Double?, String?)] = [
            ("Front left", s.tirePressureFlBar, s.tirePressureStatusFl), ("Front right", s.tirePressureFrBar, s.tirePressureStatusFr),
            ("Rear left", s.tirePressureRlBar, s.tirePressureStatusRl), ("Rear right", s.tirePressureRrBar, s.tirePressureStatusRr),
        ]
        let out = rows.map { label, v, status -> Wheel in
            let bar = v.flatMap { $0.isFinite && $0 > 0 ? $0 : nil }
            return Wheel(label: label, bar: bar, psi: bar.map { $0 * 14.5038 }, low: bar.map { $0 < lowTireBar } ?? false, rivianLow: rivianSaysLow(status))
        }
        return out.contains { $0.bar != nil } ? out : nil
    }

    /// What a wheel says under its number: Low (ours, the floor) or Low (Rivian's word), or nothing. Never "OK" or "Normal".
    static func wheelFlag(_ w: Wheel) -> String? { w.low ? "Low" : w.rivianLow ? "Low (Rivian)" : nil }

    static let tiresNote = "Pressure changes with temperature. Your truck's pressure is on the placard on the driver's door. Marked low below 32 psi or when Rivian reports it."

    /// The current version and an update, if one is pending (the feed says "0.0.0" when none is).
    static func software(_ s: VehicleState) -> (current: String, available: String?)? {
        let cur = (s.otaCurrentVersion ?? "").trimmingCharacters(in: .whitespaces)
        guard !cur.isEmpty else { return nil }
        let avail = (s.otaAvailableVersion ?? "").trimmingCharacters(in: .whitespaces)
        let none = avail.isEmpty || avail.allSatisfy { $0 == "0" || $0 == "." }
        return (cur, !none && avail != cur ? avail : nil)
    }

    /// The release-notes link: plain https with a host and no credentials, or nothing.
    static func notesURL(_ s: String?) -> URL? {
        guard let s, let u = URL(string: s), u.scheme == "https", let host = u.host, !host.isEmpty, u.user == nil, u.password == nil else { return nil }
        return u
    }

    static func isR2(_ model: String?) -> Bool { (model ?? "").trimmingCharacters(in: .whitespaces).uppercased() == "R2" }
}
