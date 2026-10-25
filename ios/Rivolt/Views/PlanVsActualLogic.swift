import Foundation

/// The words and figures of plan vs actual, with no view code so a test can see them. They follow the web's
/// (web/src/lib/planVsActual.ts): a difference only where the server gave one, never a total that is incomplete.
enum PlanVsActualLogic {
    enum State: Equatable { case none, suggested, inProgress, linkedFull, linkedPartial, linkLost }

    static func state(_ p: PlanVsActual) -> State {
        let top = p.candidates?.first
        // Linked, but the outing can't be found any more (data re-imported): say so rather than "drive it first".
        if p.status == "linked", p.comparison == nil, p.link != nil { return .linkLost }
        guard p.status != "none", let c = p.comparison else {
            if p.status == "suggested", top != nil { return top?.inProgress == true ? .inProgress : .suggested }
            return .none
        }
        // An unfinished trip isn't compared yet: a half-driven one would read as "saved money".
        if c.status == "in_progress" || top?.inProgress == true { return .inProgress }
        if p.status == "suggested" { return .suggested }
        let cost = c.metrics?.first { $0.metric == "cost" }
        return cost?.actualComplete == false ? .linkedPartial : .linkedFull
    }

    /// How sure we are the drive was this trip, in plain words.
    static func matchLabel(_ band: String?) -> String {
        switch band {
        case "high": return "Likely match"
        case "medium": return "Possible match"
        case "low": return "Weak match"
        default: return "Match"
        }
    }

    static func hm(_ minutes: Double) -> String {
        let h = Int(minutes / 60), m = Int((minutes.truncatingRemainder(dividingBy: 60)).rounded())
        return h > 0 ? "\(h) h \(m) m" : "\(m) min"
    }

    private static let labels = ["cost": "Fast-charging cost", "energy_kwh": "Energy used", "dcfc_stops": "Fast-charging stops",
                                 "arrival_soc": "Arrival battery", "duration": "Duration", "miles": "Distance"]

    private static func value(_ m: PlanVsActual.Metric, _ v: Double?, currency: String) -> String? {
        guard let v, let key = m.metric else { return nil }
        switch key {
        case "cost": return NumberText.money(v, currency)
        case "energy_kwh": return "\(NumberText.fixed(v, 0)) kWh"
        case "dcfc_stops": return NumberText.fixed(v, 0)
        case "arrival_soc": return "\(Int(v.rounded()))%"
        case "duration": return hm(v)
        case "miles": return "\(NumberText.fixed(v, 0)) mi"
        default: return nil
        }
    }

    private static func delta(_ m: PlanVsActual.Metric, currency: String) -> String? {
        guard let d = m.delta, d != 0, let key = m.metric else { return nil }
        let sign = d > 0 ? "+" : "−"
        switch key {
        case "cost": return "\(sign)\(NumberText.money(abs(d), currency))"
        case "energy_kwh": return "\(sign)\(NumberText.fixed(abs(d), 0)) kWh"
        case "dcfc_stops": return "\(sign)\(NumberText.fixed(abs(d), 0))"
        case "arrival_soc": return "\(sign)\(NumberText.fixed(abs(d), 0)) pts"
        case "duration": return "\(sign)\(NumberText.fixed(abs(d), 0)) min"
        case "miles": return "\(sign)\(NumberText.fixed(abs(d), 0)) mi"
        default: return nil
        }
    }

    struct Row: Equatable {
        var label: String
        var planned: String?
        var actual: String?
        var delta: String?
        /// More cost or time than planned; everything else is neutral (an extra stop isn't a fault).
        var watch: Bool
        var estimated: Bool
        var incomplete: String?
        var reasons: [String]
    }

    private static let reasonCopy = [
        "extra_stop": "You made an extra fast-charge stop.", "skipped_stop": "You skipped a planned stop.",
        "estimated": "Part of this total is estimated from the network's price, not billed.",
        "unpriced": "Some charges aren't priced yet, so the total is incomplete.",
        "energy_partial": "One drive wasn't fully recorded, so energy may be off.",
    ]

    /// At most two reasons, the largest amount first; a line with neither a server detail nor placeholder-free copy is dropped.
    private static func reasons(_ m: PlanVsActual.Metric) -> [String] {
        let have = (m.reasons ?? []).compactMap { r -> (PlanVsActual.Reason, String)? in
            if let d = r.detail, !d.isEmpty { return (r, d) }
            return r.code.flatMap { reasonCopy[$0] }.map { (r, $0) }
        }
        let withAmount = have.filter { $0.0.amount != nil }.sorted { abs($0.0.amount ?? 0) > abs($1.0.amount ?? 0) }
        let info = have.filter { $0.0.amount == nil }
        return (withAmount + info).prefix(2).map(\.1)
    }

    static func rows(_ c: PlanVsActual.Comparison) -> [Row] {
        let currency = c.currency ?? "USD"
        return (c.metrics ?? []).compactMap { raw in
            guard let key = raw.metric, let label = labels[key] else { return nil }
            // Rivian's planner can report 0 planned energy: that is missing, not a plan. Show the actual only.
            var m = raw
            if key == "energy_kwh", (m.planned ?? 0) <= 0 { m.planned = nil; m.delta = nil; m.reasons = [] }
            let incomplete = key == "cost" && m.actualComplete == false
            let known = m.actualKnown.map { NumberText.money($0, currency) }
            return Row(
                label: label, planned: value(m, m.planned, currency: currency),
                // An incomplete cost is never shown as "the actual".
                actual: incomplete ? nil : value(m, m.actual, currency: currency),
                delta: incomplete ? nil : delta(m, currency: currency),
                watch: !incomplete && (key == "cost" || key == "duration") && (m.delta ?? 0) > 0,
                estimated: key == "cost" && (m.actualSources?["network_estimate"] ?? 0) > 0,
                incomplete: incomplete ? "\(known.map { "\($0) so far; " } ?? "")\(m.unpricedSessions ?? 0) charges aren't priced yet" : nil,
                reasons: incomplete ? [] : reasons(m))
        }
    }

    /// The one-line verdict above the figures; nil when there is nothing honest to say yet.
    static func costHeader(_ c: PlanVsActual.Comparison) -> String? {
        let currency = c.currency ?? "USD"
        guard let m = c.metrics?.first(where: { $0.metric == "cost" }) else { return nil }
        if (m.reasons ?? []).contains(where: { $0.code == "currency_mismatch" }) {
            return "Can't compare. The plan and your charges use different currencies."
        }
        if m.actualComplete == false {
            let known = m.actualKnown.map { "\(NumberText.money($0, currency)) so far; " } ?? ""
            return "Not final yet. \(known)\(m.unpricedSessions ?? 0) charges aren't priced yet."
        }
        guard let actual = m.actual else { return nil }
        let paid = NumberText.money(actual, currency) + ((m.actualSources?["network_estimate"] ?? 0) > 0 ? " (estimated)" : "")
        guard let planned = m.planned else { return "No cost in the plan. You paid \(paid)." }
        if planned > 0, abs(actual - planned) / planned <= 0.1 { return "On plan. Planned \(NumberText.money(planned, currency)), paid \(paid)." }
        let d = NumberText.money(abs(actual - planned), currency)
        return "\(d) \(actual > planned ? "over" : "under") plan. Planned \(NumberText.money(planned, currency)), paid \(paid)."
    }

    /// "Driven Oct 28 · 281 mi".
    static func driven(_ c: PlanVsActual.Comparison, candidateMiles: Double? = nil, timeZone: TimeZone = .current) -> String {
        let miles = c.leg?.miles ?? candidateMiles
        var parts = ["Driven"]
        if let d = c.leg?.startedAt?.date { parts[0] += " " + d.formatted(Date.FormatStyle(timeZone: timeZone).month(.abbreviated).day()) }
        if let miles, miles > 0 { parts.append("\(Int(miles.rounded())) mi") }
        return parts.joined(separator: " · ")
    }

    /// "Was this your trip on Oct 28?"
    static func question(_ candidate: PlanVsActual.Candidate?, timeZone: TimeZone = .current) -> String {
        guard let d = candidate?.startedAt?.date else { return "Was this your trip?" }
        return "Was this your trip on \(d.formatted(Date.FormatStyle(timeZone: timeZone).month(.abbreviated).day()))?"
    }

    /// What to tell the person when a call failed. Never the server's own text.
    static func failure(_ error: Error) -> String {
        if case APIError.server(let status, _) = error, status == 404 { return "That saved trip is gone, so there is nothing to compare." }
        return "Couldn't reach the comparison right now. Try again in a minute."
    }
}
