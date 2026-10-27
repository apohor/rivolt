import Foundation

/// The web's Money rules (lib/ledger.ts, components/money.tsx, pages/MoneyPage.tsx, lib/savings.ts), kept out of the
/// views so they can be tested. Every figure says where its price came from: billed and entered prices are solid,
/// estimates are marked, and a missing price is a dash, never $0.
enum MoneyLogic {
    // MARK: Formatting

    /// The app's one rounding rule (`NumberText`): a half away from zero, as the web.
    static func money(_ v: Double, _ currency: String, digits: Int = 2) -> String { NumberText.money(v, currency, digits: digits) }

    /// Under a month's energy total that includes an estimated charge (the web's `ENERGY_ESTIMATED_TOTAL_NOTE`).
    static let energyEstimatedTotalNote = "Some charges this month have an estimated energy figure (marked ~): worked out from the battery's change because a Rivolt bug lost their energy count."

    static func plural(_ n: Int, _ one: String, _ many: String) -> String { n == 1 ? one : many }

    static func fixed(_ v: Double, _ digits: Int) -> String { NumberText.fixed(v, digits) }

    /// The web's `formatDistance`: whole units, and from 100 km / 60 mi up rounded to the nearest 10 km / 5 mi, so it
    /// never gives a precise position away.
    static func distance(km: Double, metric: Bool) -> String {
        let v = metric ? km : km / 1.609344
        let step: Double = metric ? 10 : 5
        let big: Double = metric ? 100 : 60
        let n = v >= big ? (v / step).rounded() * step : v.rounded()
        return "\(Int(n)) \(metric ? "km" : "mi")"
    }

    /// "2026-10" as a date in `calendar` (the 1st, at midnight).
    static func monthStart(_ period: String, calendar: Calendar) -> Date? {
        let p = period.split(separator: "-").compactMap { Int($0) }
        guard p.count == 2, (1...12).contains(p[1]) else { return nil }
        return calendar.date(from: DateComponents(year: p[0], month: p[1], day: 1))
    }

    static func period(_ month: PeriodMonth, calendar: Calendar) -> String {
        let c = calendar.dateComponents([.year, .month], from: month.start)
        return String(format: "%04d-%02d", c.year ?? 0, c.month ?? 0)
    }

    static func shift(_ period: String, by months: Int, calendar: Calendar) -> String {
        guard let d = monthStart(period, calendar: calendar), let s = calendar.date(byAdding: .month, value: months, to: d) else { return period }
        let c = calendar.dateComponents([.year, .month], from: s)
        return String(format: "%04d-%02d", c.year ?? 0, c.month ?? 0)
    }

    /// "September" (the month alone, as the delta reads it).
    static func monthName(_ period: String, calendar: Calendar) -> String {
        guard let d = monthStart(period, calendar: calendar) else { return period }
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.wide)
        f.calendar = calendar
        return d.formatted(f)
    }

    /// "September 2026".
    static func monthTitle(_ period: String, calendar: Calendar) -> String {
        guard let d = monthStart(period, calendar: calendar) else { return period }
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.wide).year()
        f.calendar = calendar
        return d.formatted(f)
    }

    /// "Oct 2026" for a tracker month (empty when it isn't a real month).
    static func monthShort(_ ym: String?) -> String {
        guard let ym else { return "" }
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        guard let d = monthStart(ym, calendar: utc), ym.count == 7 else { return "" }
        var f = Date.FormatStyle(locale: Locale(identifier: "en_US"), timeZone: utc.timeZone).month(.abbreviated).year()
        f.calendar = utc
        return d.formatted(f)
    }

    /// "Oct 20, 07:18 AM", in the phone's zone.
    static func dateTime(_ d: Date, calendar: Calendar) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.abbreviated).day().hour(.twoDigits(amPM: .abbreviated)).minute(.twoDigits)
        f.calendar = calendar
        return d.formatted(f)
    }

    /// "Sep 4".
    static func shortDay(_ d: Date, calendar: Calendar) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).month(.abbreviated).day()
        f.calendar = calendar
        return d.formatted(f)
    }

    // MARK: Prices

    static func isOpenNetworkState(_ s: String?) -> Bool { s == "estimated" || s == "needs_confirmation" }

    static func hasOpenNetworkState(_ r: LedgerReceipt) -> Bool {
        isOpenNetworkState(r.networkState) || r.sessions.contains { isOpenNetworkState($0.networkState) }
    }

    /// The month's headline: what Rivian billed plus our estimate for the rest when the server splits it, else the total.
    static func statementTotal(_ m: LedgerMonth) -> Double {
        if let b = m.spend.billedTotal, let e = m.spend.estimatedTotal { return b + e }
        return m.spend.total
    }

    /// A month's spend up to and including a day of the month, from its receipts (each counts on the day it started).
    static func spendThroughDay(_ m: LedgerMonth, day: Int) -> Double {
        (m.receipts ?? []).reduce(0) { sum, r in
            guard let d = Int(r.localDate.dropFirst(8).prefix(2)), d <= day, let c = r.cost else { return sum }
            return sum + c
        }
    }

    enum Grammar: Equatable { case recorded, free, estimated, suspect, pending, unknown }

    /// The fields the price rules read, shared by a receipt and one of its sessions.
    struct Priceable {
        let cost: Double?
        let costSource: String
        let flags: [String]
        let networkState: String?
        let sessions: [LedgerSession]?

        init(_ r: LedgerReceipt) {
            cost = r.cost; costSource = r.costSource; flags = r.flags; networkState = r.networkState; sessions = r.sessions
        }

        init(_ s: LedgerSession) {
            cost = s.cost; costSource = s.costSource; flags = s.flags; networkState = s.networkState; sessions = nil
        }
    }

    /// Counted in the total at the home rate while away from home. On a receipt its sessions decide: a pending fast
    /// charge also carries `home_rate_away` but is out of the total.
    static func isSuspect(_ r: Priceable) -> Bool {
        if let s = r.sessions { return s.contains { $0.costSource == "home_rate" && $0.flags.contains("home_rate_away") } }
        return r.costSource == "home_rate" && r.flags.contains("home_rate_away")
    }

    static func needsPrice(_ r: LedgerReceipt) -> Bool {
        r.flags.contains("unpriced") || r.flags.contains("estimate_pending")
            || r.sessions.contains { $0.cost == nil || $0.costSource == "unknown" || $0.costSource == "home_rate_misapplied" }
    }

    static func isConfirmedNetwork(_ r: Priceable) -> Bool {
        if r.networkState == "confirmed" { return true }
        let est = (r.sessions ?? []).filter { $0.costSource == "network_estimate" }
        return r.costSource == "network_estimate" && !est.isEmpty && est.allSatisfy { $0.networkState == "confirmed" }
    }

    static func grammar(_ r: Priceable) -> Grammar {
        if r.costSource == "home_rate_misapplied" { return .pending }
        if r.cost == nil || r.costSource == "unknown" { return r.flags.contains("estimate_pending") ? .pending : .unknown }
        if r.costSource == "user" && r.cost == 0 { return .free }
        if r.costSource == "observed_estimate" { return .estimated }
        if r.costSource == "network_estimate" { return isConfirmedNetwork(r) ? .recorded : .estimated }
        if r.costSource == "mixed", r.sessions?.contains(where: { $0.costSource == "network_estimate" || $0.costSource == "observed_estimate" }) == true {
            return .estimated
        }
        if isSuspect(r) { return .suspect }
        return .recorded
    }

    /// Where the price came from, in the web's words ("Billed · $0.48/kWh", "Estimate · Electrify America · $0.36/kWh").
    static func sourceLabel(_ r: LedgerReceipt, currency: String) -> String {
        let rate = r.rate.map { $0 > 0 ? " · \(money($0, currency))/kWh" : "" } ?? ""
        let net = r.network.flatMap { $0.isEmpty ? nil : " · \($0)" } ?? ""
        switch r.costSource {
        case "rivian":
            return "Billed\(rate)"
        case "home_rate":
            return r.flags.contains("home_rate_away") ? "Home rate, but away from home\(rate) · likely wrong" : "Home rate\(rate)"
        case "network_estimate":
            let word = hasOpenNetworkState(r) ? "Estimated, confirm" : isConfirmedNetwork(Priceable(r)) ? "Confirmed" : "Estimate"
            return "\(word)\(net)\(rate)"
        case "observed_estimate":
            let m = monthShort(r.estimateMonth)
            return "\(m.isEmpty ? "Estimate (typical observed price, date unknown)" : "Estimate: typical price seen by DCFC Tracker, \(m)")\(net)\(rate)"
        case "user":
            return r.cost == 0 ? "Free · entered by you" : "Entered by you\(rate)"
        case "home_rate_misapplied":
            return "Estimate pending · fast charge was priced at your home rate"
        case "mixed":
            let parts = [r.flags.contains("estimate_pending") ? "part pending" : nil, r.flags.contains("unpriced") ? "part unpriced" : nil].compactMap { $0 }
            return parts.isEmpty ? "Mixed prices" : "Mixed prices · \(parts.joined(separator: ", "))"
        default:
            return r.needsRate == true ? "Needs a price" : "Price unknown"
        }
    }

    // MARK: The month

    static func estimatedShare(_ m: LedgerMonth) -> Double {
        if let s = m.spend.estimatedShare { return s }
        return m.spend.total > 0 ? (m.spend.bySource?.networkEstimate ?? 0) / m.spend.total : 0
    }

    static func suspectSessionCount(_ m: LedgerMonth) -> Int {
        (m.receipts ?? []).reduce(0) { n, r in
            n + r.sessions.filter { $0.costSource == "home_rate" && $0.cost != nil && $0.flags.contains("home_rate_away") }.count
        }
    }

    /// With auto-network on, the fast sessions the matcher couldn't price; the server's counters when there are no rows.
    static func unidentifiedPending(_ m: LedgerMonth) -> (sessions: Int, kwh: Double) {
        let all = (m.receipts ?? []).flatMap(\.sessions)
        guard !all.isEmpty else { return (m.spend.estimatePendingSessions ?? 0, m.spend.estimatePendingKwh ?? 0) }
        let p = all.filter { $0.costSource == "home_rate_misapplied" && $0.networkState == nil }
        return (p.count, p.reduce(0) { $0 + ($1.kwh ?? 0) })
    }

    /// Auto-network is in use only when something carries it.
    static func autoNetwork(_ m: LedgerMonth, toConfirm: Int?) -> Bool {
        (toConfirm ?? 0) > 0 || (m.receipts ?? []).contains { $0.networkState != nil || $0.sessions.contains { $0.networkState != nil } }
    }

    /// "About these numbers": plain-English notes from the server's counters.
    static func notes(_ m: LedgerMonth, auto: Bool) -> [String] {
        var n: [String] = []
        let dq = m.dataQuality
        let pending = auto ? unidentifiedPending(m).sessions : (m.spend.estimatePendingSessions ?? 0)
        if pending > 0 {
            n.append(auto
                ? "Couldn't identify the network for \(pending) fast-charging \(plural(pending, "session", "sessions")); \(plural(pending, "it's", "they're")) left out of the total until you pick the network."
                : "\(pending) fast-charging \(plural(pending, "session was", "sessions were")) priced at your home rate by an older version of Rivolt. \(plural(pending, "It's", "They're")) left out of the total until you add the price.")
        }
        let suspect = suspectSessionCount(m)
        if suspect > 0 {
            n.append("\(suspect) \(plural(suspect, "session", "sessions")) away from home \(plural(suspect, "was", "were")) priced at your home rate and \(plural(suspect, "is", "are")) in the total; the real price was probably different.")
        }
        let u = m.spend.unpricedSessions
        if u > 0 { n.append("\(u) \(plural(u, "session has", "sessions have")) no price and \(plural(u, "isn't", "aren't")) in the total.") }
        let share = estimatedShare(m)
        if share > 0 { n.append("\(Int((share * 100).rounded()))% of the total is estimated from charging-network rates.") }
        if dq.supersededExcluded > 0 {
            n.append("\(dq.supersededExcluded) duplicate charging \(plural(dq.supersededExcluded, "session was", "sessions were")) excluded.")
        }
        if dq.drivesSupersededExcluded > 0 {
            n.append("\(dq.drivesSupersededExcluded) duplicate \(plural(dq.drivesSupersededExcluded, "drive was", "drives were")) excluded.")
        }
        let ne = m.energy.nullEnergySessions
        if ne > 0 {
            n.append("\(ne) \(plural(ne, "session has", "sessions have")) no energy recorded and \(plural(ne, "is", "are")) left out of kWh totals.")
        }
        if dq.openSessions > 0 {
            n.append("\(dq.openSessions) \(plural(dq.openSessions, "session is", "sessions are")) still charging; totals will change.")
        }
        if dq.currencyMismatchExcluded > 0 {
            let c = dq.currencyMismatchExcluded
            n.append("\(c) \(plural(c, "session", "sessions")) in another currency \(plural(c, "is", "are")) not in the total.")
        }
        if let cov = m.driving.coverage, m.driving.miles > 0, cov < 0.9 {
            n.append("Energy is known for \(Int((cov * 100).rounded()))% of miles driven.")
        }
        if dq.costSourceInferred {
            if let k = dq.costSourceDerivedSessions, k > 0 {
                n.append("\(k) older \(plural(k, "session's", "sessions'")) price source was worked out from your home rate rather than recorded.")
            } else {
                n.append("Some price sources were worked out from your home rate rather than recorded.")
            }
        }
        return n
    }

    // MARK: The energy split

    enum SplitKey: String { case home, away, publicAC = "public", unknown, ac, dcfc }

    struct Segment: Equatable, Identifiable {
        let key: SplitKey
        let totals: LedgerTotals
        var id: String { key.rawValue }

        var label: String {
            switch key {
            case .home: return "Home"
            case .away, .publicAC: return "Away"
            case .unknown: return "Unknown place"
            case .ac: return "AC"
            case .dcfc: return "Fast"
            }
        }
    }

    struct SplitBar: Equatable, Identifiable {
        let label: String
        let segments: [Segment]
        var id: String { label }
    }

    /// One "Charging" bar of Home / Away / Fast when the server sends place-by-kind cells; otherwise its two older bars.
    static func splitBars(_ m: LedgerMonth) -> [SplitBar] {
        let e = m.energy
        if let pk = e.byPlaceKind {
            var s = [Segment(key: .home, totals: pk.home.ac), Segment(key: .publicAC, totals: pk.away.ac), Segment(key: .dcfc, totals: e.byKind.dcfc)]
            if pk.unknown.ac.sessions > 0 { s.append(Segment(key: .unknown, totals: pk.unknown.ac)) }
            return [SplitBar(label: "Charging", segments: s)]
        }
        let how = SplitBar(label: "How", segments: [Segment(key: .ac, totals: e.byKind.ac), Segment(key: .dcfc, totals: e.byKind.dcfc)])
        guard let p = e.byPlace else { return [how] }
        var w = [Segment(key: .home, totals: p.home), Segment(key: .away, totals: p.away)]
        if p.unknown.sessions > 0 { w.append(Segment(key: .unknown, totals: p.unknown)) }
        return [SplitBar(label: "Where", segments: w), how]
    }

    // MARK: Receipts

    enum Filter: String, CaseIterable, Identifiable {
        case all, home, away, dcfc, confirm
        var id: String { rawValue }

        var label: String {
            switch self {
            case .all: return "All"
            case .home: return "Home"
            case .away: return "Away"
            case .dcfc: return "Fast"
            case .confirm: return "To confirm"
            }
        }

        /// "To confirm" lists charges from every month, so no receipt matches it.
        func matches(_ r: LedgerReceipt) -> Bool {
            switch self {
            case .all: return true
            case .dcfc: return r.kind == "dcfc"
            case .confirm: return false
            case .home: return r.kind == "ac" && r.place == "home"
            case .away: return r.kind == "ac" && r.place == "away"
            }
        }

        static func shown(current: Filter, toConfirm: Int?) -> [Filter] {
            current == .confirm || (toConfirm ?? 0) > 0 ? allCases : [.all, .home, .away, .dcfc]
        }
    }

    static let flagText: [String: String] = [
        "null_energy": "no energy recorded",
        "open_session": "charging now",
        "abandoned": "ended abruptly",
        "continues_next_month": "continues next month",
        "continued_from_previous_month": "started last month",
        "currency_mismatch": "other currency, not in total",
    ]

    // MARK: Off-peak savings

    enum SavingsView: Equatable {
        enum Need: Equatable { case schedule, homeRate, sessions }
        case needs(Need)
        /// The person said their electricity is one flat rate: no off-peak hours, no estimate.
        case flat(basis: String)
        case nothing(leftOut: Int, reasons: [String])
        case saved(saved: Double, moved: Int, leftOut: Int, asCharged: Double?, ifMoved: Double?, reasons: [String], considered: Int?, basis: String)
    }

    /// A reason key as plain words. The keys arrive camel-cased by the decoder, so both spellings are read.
    static func reasonText(_ key: String) -> String {
        switch key {
        case "plugged_in_too_briefly", "pluggedInTooBriefly": return "plugged in too briefly for the charge to fit in one stretch"
        case "no_cheaper_hours", "noCheaperHours": return "already at the cheapest hours it could use"
        default:
            var out = ""
            for ch in key {
                if ch == "_" || ch == "-" { out.append(" ") } else if ch.isUppercase { out.append(" "); out.append(Character(ch.lowercased())) } else { out.append(ch) }
            }
            return out.split(separator: " ").joined(separator: " ").lowercased()
        }
    }

    /// Whether Money draws the Off-peak savings card: not when the answer failed, and not for a flat rate (no off-peak hours, no
    /// estimate, so nothing to say; the choice is undone in Settings → Charging → Off-peak schedule).
    static func showsSavingsCard(_ d: LedgerSavings?, failed: Bool) -> Bool {
        if failed { return false }
        if let d, case .flat = savingsView(d) { return false }
        return true
    }

    static func savingsView(_ d: LedgerSavings) -> SavingsView {
        if d.reason == "flat_rate" || d.flatRate == true { return .flat(basis: d.basis ?? "") }
        if d.needs == "offpeak_schedule" { return .needs(.schedule) }
        if d.needs == "home_rate" { return .needs(.homeRate) }
        if d.needs != nil { return .needs(.sessions) }
        guard let saved = d.couldHaveSaved else { return .needs(.sessions) }
        let reasons = (d.leftOutReasons ?? [:]).filter { $0.value > 0 }.sorted { $0.value != $1.value ? $0.value > $1.value : $0.key < $1.key }
            .map { "\(reasonText($0.key)) (\($0.value))" }
        let leftOut = d.sessionsLeftOut ?? 0
        let moved = d.sessionsMoved ?? 0
        if saved <= 0 || moved == 0 { return .nothing(leftOut: leftOut, reasons: reasons) }
        return .saved(saved: saved, moved: moved, leftOut: leftOut, asCharged: d.asChargedTotal, ifMoved: d.ifMovedTotal, reasons: reasons,
                      considered: d.sessionsConsidered, basis: d.basis ?? "")
    }

    static func savedHeadline(moved: Int, money: String) -> String {
        "Moving \(moved) home \(plural(moved, "charge", "charges")) into your off-peak windows could have saved about \(money) (estimate)."
    }

    static func leftOutLine(_ n: Int) -> String {
        "\(n) \(plural(n, "session", "sessions")) could not be moved and \(plural(n, "is", "are")) left out."
    }
}

// MARK: - The statement at the top

/// A web page the phone opens in the browser (pricing and rates are edited on the web).
enum MoneyLink: Equatable {
    case homeRate, settings, networkRates, offpeak
    case charge(String, pricing: Bool)

    var path: String {
        switch self {
        case .homeRate: return "/settings?tab=charging#cost"
        case .settings: return "/settings?tab=charging"
        case .networkRates: return "/settings?tab=charging#networks"
        case .offpeak: return "/settings?tab=charging#offpeak"
        case .charge(let id, let pricing):
            let escaped = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) ?? id
            return "/charges/\(escaped)\(pricing ? "#pricing" : "")"
        }
    }
}

/// The hero card's lines for one month, as the web's `Statement` words them.
struct MoneyStatement: Equatable {
    struct Issue: Equatable {
        let lead: String?
        let text: String
        let amber: Bool
        var link: (label: String, to: MoneyLink, after: String)?

        static func == (a: Issue, b: Issue) -> Bool {
            a.lead == b.lead && a.text == b.text && a.amber == b.amber && a.link?.label == b.link?.label && a.link?.to == b.link?.to && a.link?.after == b.link?.after
        }
    }

    let currency: String
    let headline: Double
    let isCurrentMonth: Bool
    let partialFrom: Date?
    /// The change against the previous month (or the same days of it), and what it is against ("September 1–21").
    let delta: Double?
    let deltaLabel: String
    /// "spent on charging · 319 kWh · 902 mi · $0.055/mi".
    let subline: String
    /// Under the month's energy total when it includes a charge whose energy was estimated.
    let energyNote: String?
    /// "$0 doesn't mean charging was free": sessions, kWh and whether the home rate is missing.
    let zero: (sessions: Int, kwh: Double, noRate: Bool)?
    let toConfirm: Int?
    let billed: (billed: Double, billedSessions: Int?, estimated: Double, estimatedSessions: Int?)?
    let observed: (cost: Double, sessions: Int)?
    let unpricedFast: Int?
    let estimatedPercent: Int?
    let issues: [Issue]
    let notes: [String]
    let showObservedNotice: Bool
    let empty: Bool

    static func == (a: MoneyStatement, b: MoneyStatement) -> Bool {
        a.headline == b.headline && a.delta == b.delta && a.deltaLabel == b.deltaLabel && a.subline == b.subline && a.issues == b.issues && a.notes == b.notes
            && a.partialFrom == b.partialFrom && a.toConfirm == b.toConfirm && a.empty == b.empty
    }

    /// `months` are the summary's (oldest first); `previous` is the previous month in full, read only for a month in
    /// progress so it is compared with the same days.
    init(month m: LedgerMonth, months: [LedgerMonth], previous: LedgerMonth?, toConfirm: Int?, now: Date, calendar: Calendar) {
        typealias L = MoneyLogic
        let cur = m.currency
        currency = cur
        let headline = L.statementTotal(m)
        self.headline = headline
        isCurrentMonth = m.isCurrentMonth
        self.toConfirm = toConfirm
        let auto = L.autoNetwork(m, toConfirm: toConfirm)
        let unid = L.unidentifiedPending(m)
        let pending = auto ? unid.sessions : (m.spend.estimatePendingSessions ?? 0)
        let pendingKwh = auto ? unid.kwh : (m.spend.estimatePendingKwh ?? 0)
        let suspect = L.suspectSessionCount(m)
        notes = L.notes(m, auto: auto)

        // A month in progress is compared with the same days of the previous one; a previous month with only part of
        // its days of data is not compared at all. Nothing priced: no delta and no $/mi.
        let prevPeriod = L.shift(m.period, by: -1, calendar: calendar)
        let prev = months.first { $0.period == prevPeriod }
        let hasCharging = m.spend.pricedSessions > 0 || headline > 0
        let sameDay = m.isCurrentMonth && prev != nil
        let day = calendar.component(.day, from: now)
        let twoDays: TimeInterval = 2 * 86_400
        let prevPartial = prev.flatMap { p in p.firstActivityAt.map { $0.timeIntervalSince(p.range.start) > twoDays } } ?? false
        let base: Double? = sameDay ? previous.map { L.spendThroughDay($0, day: day) } : prevPartial ? nil : prev?.spend.total
        delta = hasCharging && (base ?? 0) > 0 ? (headline - base!) / base! : nil
        deltaLabel = prev.map { sameDay ? "\(L.monthName($0.period, calendar: calendar)) 1–\(day)" : L.monthName($0.period, calendar: calendar) } ?? ""

        // Partial month: the first month with any data, where activity starts well after the 1st.
        let earliest = months.first { $0.firstActivityAt != nil }
        if let first = m.firstActivityAt, earliest == nil || earliest?.period == m.period, first.timeIntervalSince(m.range.start) > twoDays {
            partialFrom = first
        } else {
            partialFrom = nil
        }

        var sub = "spent on charging · \(m.energy.includesEstimated == true ? "~" : "")\(L.fixed(m.energy.kwhAdded, 0)) kWh"
        if m.driving.miles > 0 { sub += " · \(L.fixed(m.driving.miles, 0)) mi" }
        if hasCharging, let cpm = m.driving.costPerMile { sub += " · \(L.money(cpm, cur, digits: 3))/mi" }
        subline = sub
        energyNote = m.energy.includesEstimated == true ? MoneyLogic.energyEstimatedTotalNote : nil

        let s = m.spend
        let noRate = s.unpricedHomeRateEstimate == nil && s.unpricedKwh > 0
        zero = headline == 0 && s.unpricedSessions > 0 ? (s.unpricedSessions, s.unpricedKwh, noRate) : nil
        if let b = s.billedTotal, let e = s.estimatedTotal {
            billed = (b, m.billedSessions, e, m.estimatedSessions)
        } else {
            billed = nil
        }
        let obs = s.observedEstimateSessions ?? 0
        observed = obs > 0 ? (s.observedEstimateCost ?? 0, obs) : nil
        let fast = s.unpricedFastSessions ?? 0
        unpricedFast = fast > 0 ? fast : nil
        showObservedNotice = obs > 0 || fast > 0
        let share = L.estimatedShare(m)
        estimatedPercent = share > 0 && s.billedTotal == nil ? Int((share * 100).rounded()) : nil

        var issues: [Issue] = []
        let p = L.plural
        if auto && pending + suspect > 0 {
            let n = pending + suspect
            var t = " for \(n) \(p(n, "session", "sessions"))"
            if pending > 0 { t += " (\(pending) \(p(pending, "isn't", "aren't")) in the total yet, \(L.fixed(pendingKwh, 1)) kWh)" }
            t += ". Pick the network to price \(p(n, "it", "them"))."
            issues.append(Issue(lead: "Couldn't identify the network", text: t, amber: true))
        }
        if !auto && pending > 0 {
            issues.append(Issue(lead: "Estimate pending:", text: " \(pending) fast-charging \(p(pending, "session", "sessions")) (\(L.fixed(pendingKwh, 1)) kWh) \(p(pending, "isn't", "aren't")) in the total yet. An older version priced \(p(pending, "it", "them")) at your home rate. Add the price.", amber: true))
        }
        if !auto && suspect > 0 {
            issues.append(Issue(lead: "Likely wrong:", text: " \(suspect) \(p(suspect, "session", "sessions")) away from home \(p(suspect, "was", "were")) priced at your home rate and \(p(suspect, "is", "are")) in the total.", amber: true))
        }
        if s.unpricedSessions > 0 && !(headline == 0 && noRate) {
            var t = "Not included: \(s.unpricedSessions) unpriced \(p(s.unpricedSessions, "session", "sessions")) (\(L.fixed(s.unpricedKwh, 1)) kWh)"
            var link: (String, MoneyLink, String)?
            if let est = s.unpricedHomeRateEstimate, est > 0 {
                t += " ≈ \(L.money(est, cur)) if charged at home"
            } else if noRate {
                t += ". No home rate is set; "
                link = ("set it in Settings", .settings, " to price home charging.")
            }
            issues.append(Issue(lead: nil, text: t, amber: false, link: link))
        }
        self.issues = issues
        empty = (m.receipts ?? []).isEmpty && s.total == 0
    }
}

// MARK: - One receipt row

/// What a receipt row shows besides its figures: the price wording, the one action and the notes.
struct MoneyReceiptLine {
    enum Value: Equatable { case billed(String), pending, dash, free, estimated(String), suspect(String), recorded(String) }
    enum Label: Equatable { case billed, partly(String), plain(prefix: Bool, text: String, amber: Bool) }

    let value: Value
    let label: Label
    /// "Set your rate for Electrify America →" (to the network rates in Settings) or "Add price →" and its kin (to the charge).
    let rateAction: String?
    let action: (label: String, to: MoneyLink)?
    let notes: [String]
    let extra: Int
    let chip: (label: String, key: MoneyLogic.SplitKey)

    init(_ r: LedgerReceipt, currency: String) {
        typealias L = MoneyLogic
        let g = L.grammar(L.Priceable(r))
        if let b = r.billedCost {
            value = .billed(L.money(b, r.billedCurrency ?? currency))
        } else {
            switch g {
            case .pending: value = .pending
            case .unknown: value = .dash
            case .free: value = .free
            case .estimated: value = r.cost.map { .estimated(L.money($0, currency)) } ?? .dash
            case .suspect: value = r.cost.map { .suspect(L.money($0, currency)) } ?? .dash
            case .recorded:
                // A cost that follows from an estimated energy figure is an estimate too.
                value = r.cost.map { r.energyEstimated == true ? .estimated(L.money($0, currency)) : .recorded(L.money($0, currency)) } ?? .dash
            }
        }

        let billedMode = r.billedSessions != nil
        let billedPart = r.sessions.reduce(0) { $0 + ($1.billedCost ?? 0) }
        let partly = billedMode && r.billedCost == nil && (r.billedSessions ?? 0) > 0 && (r.estimatedSessions ?? 0) > 0 && billedPart > 0
        if billedMode && r.billedCost != nil {
            label = .billed
        } else if partly {
            let cur = r.sessions.first { $0.billedCost != nil }?.billedCurrency ?? currency
            label = .partly(L.money(billedPart, cur))
        } else {
            let prefix = billedMode && r.costSource != "rivian" && g != .unknown && g != .pending && g != .free
            label = .plain(prefix: prefix, text: L.sourceLabel(r, currency: currency), amber: g == .suspect || g == .pending)
        }

        // One action per row: a missing or pending price outranks a likely wrong one.
        let addPrice = g == .unknown || g == .pending || L.needsPrice(r)
        let confirm = L.hasOpenNetworkState(r)
        let rateSession = r.sessions.first { $0.needsRate == true }
        let needsRate = r.needsRate == true || rateSession != nil
        let rateName = [r.network, rateSession?.network].compactMap { $0 }.first { !$0.isEmpty }
        let observed = r.costSource == "observed_estimate" || r.sessions.contains { $0.costSource == "observed_estimate" }
        rateAction = needsRate ? "Set your rate\(rateName.map { " for \($0)" } ?? "") →" : observed ? "Set your rate →" : nil
        let parent = r.sessions.first?.chargeId ?? String(r.receiptId.trimmingPrefix("rcpt_"))
        let target = addPrice
            ? r.sessions.first { $0.cost == nil || $0.costSource == "unknown" || $0.costSource == "home_rate_misapplied" }
            : r.sessions.first { $0.costSource == "home_rate" && $0.flags.contains("home_rate_away") }
        let fixID = target?.chargeId ?? (confirm ? r.sessions.first { L.isOpenNetworkState($0.networkState) }?.chargeId : nil) ?? parent
        let word: String? = needsRate ? nil : addPrice ? "Add price →" : g == .suspect ? "Fix price →" : confirm ? "Confirm →" : nil
        action = word.map { ($0, .charge(fixID, pricing: !addPrice && g != .suspect)) }

        var notes = r.flags.compactMap { L.flagText[$0] }
        if r.spansDays > 1 { notes.insert("\(r.spansDays) days", at: 0) }
        self.notes = notes
        extra = r.sessionCount - 1
        if r.kind == "dcfc" { chip = ("Fast", .dcfc) } else if r.place == "home" { chip = ("Home", .home) } else if r.place == "away" { chip = ("Away", .away) } else { chip = ("AC", .unknown) }
    }
}
