import Foundation

/// The status chip beside the "Now" title (design_preview now_v2): what the truck is doing, as the web's. It names the state and
/// never says "Live": that word is the button to the live view.
enum NowChip {
    static func text(for s: NowSummary, clock: Date) -> String {
        let pill = NowCard.pill(for: s, clock: clock)
        let offline = s.presence == .offline
        let stale = s.presence != .live || s.freshness == .stale
        let time = { (d: Date) in d.formatted(date: .omitted, time: .shortened) }
        if !offline {
            switch s.kind {
            case .asleep:
                if let since = s.since { return "Asleep · since \(time(since))" }
            case .charging:
                var parts = ["Charging"]
                if let kw = s.chargePowerKw, kw > 0 { parts.append("\(NumberText.fixed(kw, kw < 10 ? 1 : 0)) kW") }
                if let eta = s.chargeEta {
                    let done = (s.chargeTargetPct ?? 100) < 100
                    parts.append("\(done ? "done" : "full") by \(time(clock.addingTimeInterval(Double(eta.minutes) * 60)))")
                }
                return parts.joined(separator: " · ")
            case .driving:
                if let kph = s.speedKph { return "Driving · \(NowFormat.speed(mph: kph / 1.609344))" }
                return "Driving"
            case .parked, .plugged:
                if !stale, let since = s.since { return "\(s.kind == .plugged ? "Plugged in" : "Parked") · since \(time(since))" }
            case .unknown: break
            }
        }
        // A stale or offline truck keeps the pill's own "Last seen ..."; the pill's "Live" becomes the state's name.
        if pill.text == "Live" { return s.kind == .plugged ? "Plugged in" : s.kind == .asleep ? "Asleep" : "Parked" }
        return pill.text
    }
}
