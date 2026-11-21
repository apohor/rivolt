import Foundation

/// The state slot of the redesigned Now hero (design_preview key now_slot, as the web's): under the gauge, in place of the truck's picture
/// and the rows, the figures of what the truck is doing. Charging: power, when it reaches the limit, then the session. Driving: speed,
/// then the drive so far. Parked or plugged in: where the truck is on the map, then the weather there. Only values that exist are shown;
/// nothing is a placeholder. Worked out here, without view code, so it can be tested.
struct NowSlotModel: Equatable {
    struct Stat: Equatable {
        let label: String
        let value: String
        let note: String?
        /// Our own figure, worked out here rather than the truck's: the soft card says so once in words (`estimatedHeading`) or, in a mixed set, with a small "est." on the cell.
        var estimated = false
    }

    /// Both grids name their figures once: "Estimated so far" when every cell but the clock facts (elapsed, plugged in) is ours, none otherwise (then each derived cell carries "est.").
    static func estimatedHeading(_ stats: [Stat]) -> Bool {
        let counted = stats.filter { $0.label != "Elapsed" && $0.label != "Plugged in" }
        return !counted.isEmpty && counted.allSatisfy(\.estimated)
    }

    struct Weather: Equatable {
        let temp: String
        /// "Sunny here now".
        let condition: String?
        /// An SF Symbol name.
        let symbol: String?
        let wind: String?
    }

    enum Content: Equatable {
        case parked(lat: Double, lon: Double, label: String?, weather: Weather?)
        case figures(big: String, unit: String, sub: String?, small: String?, stats: [Stat])
    }

    /// A chip beside the big figure: the charger kind while charging, the gear and the drive mode while driving.
    struct Chip: Equatable {
        let text: String
        let accent: Bool
    }

    let content: Content
    var chips: [Chip] = []
    /// Asleep only: "Asleep for 2 h 15 min" and "Asleep today 18 h 40 min", each when the server sent it, shown under the map.
    var sleep: [Stat] = []
    /// Where the truck is, in one line, for the card that has the parked graph instead of the map (now_parked_graph): "At Home" (the saved
    /// place's name) at home, "Away · 12 mi from home" away, nil when there is no saved home. As the web's, and nothing more precise than the map's label.
    var place: String?

    /// The saved home, for the "Home" label on the map and the home charger.
    struct Home: Equatable {
        let lat: Double
        let lon: Double
        let label: String?
    }

    /// Within this of the saved home counts as home (the server's and the web's radius).
    static let homeRadiusM = 250.0
    /// A session's peak at or over this is a fast charger.
    static let fastPeakKw = 50.0

    var isParked: Bool {
        if case .parked = content { return true }
        return false
    }

    /// The web's `formatDistance` with `fine`: whole numbers, tenths under 10, steps of 5 mi (10 km) from 60 mi (100 km).
    static func distanceWords(km: Double, metric: Bool) -> String {
        let v = metric ? km : km / 1.609344
        let step = metric ? 10.0 : 5.0, big = metric ? 100.0 : 60.0
        let n: String
        if v >= big { n = String(Int((v / step).rounded()) * Int(step)) }
        else if v < 10 { n = String(format: "%.1f", v) }
        else { n = String(Int(v.rounded())) }
        return "\(n) \(metric ? "km" : "mi")"
    }

    /// The server's drive, or the live drive's figures when the server has none.
    struct Drive: Equatable {
        let distanceMi: Double
        let elapsedSec: Double
        let energyKwh: Double?
        let miPerKwh: Double?
    }

    /// Whether the card shows a slot at all: an asleep truck always (sleeping for hours is normal and its position does not change, so the
    /// stale 30 min and offline 45 min limits do not hide it); every other state only while its frame is live.
    static func shown(summary: NowSummary) -> Bool {
        if summary.kind == .asleep || (summary.kind == .plugged && summary.sleeping) { return true }
        return summary.presence == .live
    }

    /// The map's fix: the demo's sample point, then the live state's, then the server's last recorded position (a sleeping truck may have no live one).
    static func fix(snapshot: NowSnapshot?, truck: (lat: Double, lon: Double)?) -> (lat: Double, lon: Double)? {
        if let l = snapshot?.location { return (l.lat, l.lon) }
        if let truck { return truck }
        return snapshot?.lastFix.map { ($0.lat, $0.lon) }
    }

    /// Nil when the state has no slot, so the picture stays: charging without a power reading, driving without a speed, parked without a fix.
    static func make(summary: NowSummary, snapshot: NowSnapshot?, session: LiveSession?, charge: ChargeBrief?, liveDrive: LiveDrive?,
                     fix: (lat: Double, lon: Double)?, home: Home?, gear: String? = nil, mode: String? = nil,
                     soft: Bool = false, parkedGraph: Bool = false, metric: Bool = NowFormat.usesMetric, clock: Date, calendar: Calendar = .current) -> NowSlotModel? {
        let atHome = home != nil && fix != nil && ChargerPin.distanceM(home!.lat, home!.lon, fix!.lat, fix!.lon) <= homeRadiusM
        switch summary.kind {
        case .charging:
            guard let detail = ChargingDetail.make(summary: summary, session: session, charge: charge), let kw = detail.powerKw else { return nil }
            // As the web: fast at a session peak of 50 kW or more, else the home charger at home, else public only with a saved home, a fix away from it and a known peak.
            let peak = max(snapshot?.charge?.peakKw ?? 0, kw)
            // The truck's own Rivian-charger flag wins, as on the web's Live page and Now card; only an active session's, an ended one is the previous plug-in's.
            let kind = session?.active == true && session?.isRivianCharger == true ? "Rivian charger" : peak >= fastPeakKw ? "Fast charger" : atHome ? "Home charger" : home != nil && fix != nil && snapshot?.charge?.peakKw != nil ? "Public charger" : nil
            return NowSlotModel(content: charging(summary: summary, detail: detail, kw: kw, snapshot: snapshot, soft: soft, clock: clock),
                                chips: kind.map { [Chip(text: $0, accent: true)] } ?? [])
        case .driving:
            guard let kph = summary.speedKph else { return nil }
            var chips: [Chip] = []
            if let gear, !gear.isEmpty { chips.append(Chip(text: gear.uppercased(), accent: false)) }
            if let mode, !mode.isEmpty { chips.append(Chip(text: driveModeWords(mode), accent: true)) }
            return NowSlotModel(content: driving(kph: kph, drive: drive(snapshot: snapshot, live: liveDrive, packKwh: summary.packKwh)), chips: chips)
        // A sleeping truck is parked: the map at its last fix is what is worth showing then.
        case .parked, .plugged, .asleep:
            guard let fix else { return nil }
            let hour = calendar.component(.hour, from: clock)
            let weather = snapshot?.ambient.map { weatherView($0, day: hour >= 6 && hour < 20) }
            var sleep: [Stat] = []
            if summary.kind == .asleep {
                if let since = snapshot?.asleepSince, clock >= since { sleep.append(Stat(label: "Asleep for", value: NowFormat.duration(clock.timeIntervalSince(since)), note: nil)) }
                // With the parked graph the figure is one of its tiles, so the slot does not say it twice.
                if !parkedGraph, let today = snapshot?.asleepTodayS, today >= 0 { sleep.append(Stat(label: "Asleep today", value: NowFormat.duration(today), note: nil)) }
            }
            var place: String?
            if atHome { place = "At \(home?.label.flatMap { $0.isEmpty ? nil : $0 } ?? "Home")" }
            else if let home { place = "Away · \(distanceWords(km: ChargerPin.distanceM(home.lat, home.lon, fix.lat, fix.lon) / 1000, metric: metric)) from home" }
            return NowSlotModel(content: .parked(lat: fix.lat, lon: fix.lon, label: atHome ? (home?.label ?? "Home") : nil, weather: weather), sleep: sleep, place: place)
        default:
            return nil
        }
    }

    // MARK: Charging

    private static let costNotes = ["rivian": "billed by Rivian", "user": "your price", "home_rate": "at your home rate",
                                    "network_estimate": "network rate", "observed_estimate": "from similar sessions"]

    private static func charging(summary: NowSummary, detail: ChargingDetail, kw: Double, snapshot: NowSnapshot?, soft: Bool, clock: Date) -> Content {
        var sub: String?
        var small: String?
        if let eta = summary.chargeEta {
            let when = clock.addingTimeInterval(TimeInterval(eta.minutes) * 60).formatted(date: .omitted, time: .shortened)
            let limit = summary.chargeTargetPct
            if soft { sub = nil } else if let limit, limit < 100 { sub = "Reaches \(Int(limit.rounded()))% at \(when)" } else { sub = "Full at \(when)" }
            small = "\(NowFormat.duration(TimeInterval(eta.minutes) * 60)) left\(eta.source == .truck ? "" : " (est.)")"
                + (soft ? "" : limit.map { " · target \(Int($0.rounded()))%" } ?? "")
        }
        let stats = chargingStats(summary: summary, detail: detail, snapshot: snapshot, soft: soft, clock: clock)
        return .figures(big: NumberText.fixed(kw, kw < 10 ? 1 : 0), unit: "kW", sub: sub, small: small, stats: stats)
    }

    /// The charge's figures: the truck's own when its session reports them, else worked out from the battery (est.), the Now card's rule. The Live screen
    /// shows the same figures (`liveStats`).
    static func chargingStats(summary: NowSummary, detail: ChargingDetail, snapshot: NowSnapshot?, soft: Bool, clock: Date) -> [Stat] {
        var stats: [Stat] = []
        if let kwh = detail.energyKwh, kwh > 0 {
            stats.append(Stat(label: "Added", value: "+\(NowFormat.oneDecimal(kwh)) kWh", note: nil, estimated: detail.energyEstimated))
        }
        // The truck's own figures come from the live session; without one (the demo, a home charge) the battery's gain at today's km per percent, marked est.
        let started = summary.chargeStartedAt ?? snapshot?.charge?.startedAt
        let elapsed: TimeInterval? = detail.elapsed ?? started.flatMap { clock > $0 ? clock.timeIntervalSince($0) : nil }
        var rangeKm = detail.rangeAddedKm.flatMap { $0 > 0 ? $0 : nil }
        let estimated = rangeKm == nil
        if estimated, let added = summary.chargeAddedPct, added > 0, let soc = summary.socPct, soc > 0, let range = summary.rangeKm {
            rangeKm = range / soc * added
        }
        if let km = rangeKm { stats.append(Stat(label: "Range added", value: "+\(NowFormat.slotDistance(mi: km / 1.609344))", note: nil, estimated: estimated)) }
        let truckRate = detail.rateKmPerHour.flatMap { $0 > 0 ? $0 : nil }
        let rate = truckRate ?? rangeKm.flatMap { km in elapsed.flatMap { $0 >= 300 ? km / ($0 / 3600) : nil } }
        if let rate { stats.append(Stat(label: "Rate", value: "\(NowFormat.slotDistance(mi: rate / 1.609344))/h", note: nil, estimated: truckRate == nil)) }
        if let elapsed { stats.append(Stat(label: "Elapsed", value: NowFormat.duration(elapsed), note: nil)) }
        // The cost so far: Rivian's latest session price when it has one (newer than the charge row's snapshot of it, so Now and Live say the same
        // figure), else the server's (`/now` charge), as the web: est. when it is an estimate, with where it comes from.
        if case .billed(let amount, let currency)? = detail.cost {
            stats.append(Stat(label: "Cost so far", value: NowFormat.money(amount, currency: currency.isEmpty ? "USD" : currency), note: "billed by Rivian", estimated: false))
        } else if let c = snapshot?.charge, let cost = c.cost {
            let estimate = c.costIsEstimate != false
            // An estimated cost always names its rate: a missing or unknown source says so rather than nothing (the web's costNote).
            let source = c.costSource.flatMap { costNotes[$0] } ?? (estimate ? "rate not known" : nil)
            let note = [estimate ? "estimate" : nil, source].compactMap { $0 }.joined(separator: " · ")
            stats.append(Stat(label: "Cost so far", value: "\(NowFormat.money(cost, currency: c.currency ?? "USD"))", note: note.isEmpty ? nil : note, estimated: estimate))
        }
        if !soft, let started { stats.append(Stat(label: "Plugged in", value: started.formatted(date: .omitted, time: .shortened), note: nil)) }
        return stats
    }

    /// The Live screen's figures for a charge, in the web Live card's order and words (Rate, Range added, Energy, Elapsed, Remaining, Cost so far): the
    /// Now card's (`chargingStats`, no "Plugged in") plus the time left (the truck's, else the estimate). Empty when the truck is not charging. Whatever
    /// has no figure is left out, never a dash.
    static func liveStats(summary: NowSummary, snapshot: NowSnapshot?, session: LiveSession?, charge: ChargeBrief?, clock: Date) -> [Stat] {
        guard let detail = ChargingDetail.make(summary: summary, session: session, charge: charge) else { return [] }
        var stats = chargingStats(summary: summary, detail: detail, snapshot: snapshot, soft: true, clock: clock)
        if let session, session.timeRemainingSeconds > 0 {
            stats.append(Stat(label: "Remaining", value: NowFormat.duration(session.timeRemainingSeconds), note: nil))
        } else if let eta = summary.chargeEta {
            stats.append(Stat(label: "Remaining", value: NowFormat.duration(TimeInterval(eta.minutes) * 60), note: nil, estimated: eta.source != .truck))
        }
        let order = ["Rate", "Range added", "Energy", "Elapsed", "Remaining", "Cost so far"]
        func rank(_ label: String) -> Int { order.firstIndex(of: label) ?? order.count }
        return stats.map { $0.label == "Added" ? Stat(label: "Energy", value: $0.value, note: $0.note, estimated: $0.estimated) : $0 }
            .sorted { rank($0.label) < rank($1.label) }
    }

    // MARK: Driving

    static func drive(snapshot: NowSnapshot?, live: LiveDrive?, packKwh: Double? = nil) -> Drive? {
        if let d = snapshot?.drive {
            return Drive(distanceMi: d.distanceMi, elapsedSec: d.durationS, energyKwh: d.energyKwh, miPerKwh: d.efficiencyMiPerKwh)
        }
        guard let d = live, d.distanceMi > 0 else { return nil }
        // As the Live card: no efficiency under a mile, or while the battery has not moved a percent (the figure would be rounding).
        let moved = packKwh.map { d.energyUsedKwh >= $0 / 100 } ?? (d.energyUsedKwh > 0)
        let eff = d.distanceMi >= 1 && moved && d.miPerKwh > 0 ? d.miPerKwh : nil
        return Drive(distanceMi: d.distanceMi, elapsedSec: d.elapsedSec, energyKwh: d.energyUsedKwh > 0 ? d.energyUsedKwh : nil, miPerKwh: eff)
    }

    private static func driving(kph: Double, drive: Drive?) -> Content {
        let metric = NowFormat.usesMetric
        var stats: [Stat] = []
        if let d = drive {
            if d.distanceMi > 0 { stats.append(Stat(label: "Distance", value: NowFormat.slotDistance(mi: d.distanceMi), note: nil)) }
            if d.elapsedSec > 0 { stats.append(Stat(label: "Time", value: NowFormat.duration(d.elapsedSec), note: nil)) }
            if let eff = d.miPerKwh, eff > 0 { stats.append(Stat(label: "Efficiency", value: NowFormat.efficiency(miPerKwh: eff), note: nil)) }
            if let kwh = d.energyKwh, kwh > 0 { stats.append(Stat(label: "Energy used", value: "\(NowFormat.oneDecimal(kwh)) kWh", note: nil)) }
        }
        return .figures(big: NumberText.fixed(metric ? kph : kph / 1.609344, 0), unit: metric ? "km/h" : "mph",
                        sub: stats.isEmpty ? nil : "This drive so far", small: nil, stats: stats)
    }

    // MARK: Weather

    private static let conditions: [String: (label: String, symbol: String)] = [
        "clear": ("Clear", "sun.max"), "partly_cloudy": ("Partly cloudy", "cloud.sun"), "cloudy": ("Cloudy", "cloud"),
        "fog": ("Fog", "cloud.fog"), "drizzle": ("Drizzle", "cloud.drizzle"), "rain": ("Rain", "cloud.rain"),
        "snow": ("Snow", "cloud.snow"), "thunder": ("Thunderstorms", "cloud.bolt"),
    ]

    /// Only what the server sent. By day a clear sky is "Sunny"; there is no rain forecast.
    static func weatherView(_ w: NowSnapshot.Ambient, day: Bool) -> Weather {
        let metric = NowFormat.usesMetric
        let known = w.condition.flatMap { conditions[$0] }
        let label = known.map { w.condition == "clear" && day ? "Sunny" : $0.label }
        let symbol = w.condition == "clear" && !day ? "moon" : known?.symbol
        return Weather(
            temp: NowComfort.wholeDegrees(celsius: w.tempC),
            condition: label.map { "\($0) here now" },
            symbol: symbol,
            wind: w.windKph.map { "Wind \(metric ? "\(Int($0.rounded())) km/h" : "\(Int(($0 / 1.609344).rounded())) mph")" })
    }

    /// The drive mode as the truck's app words it.
    static func driveModeWords(_ mode: String) -> String {
        let names = ["everyday": "All-Purpose", "sport": "Sport", "distance": "Conserve", "winter": "Snow", "towing": "Towing",
                     "off_road_auto": "All-Terrain", "off_road_sand": "Soft Sand", "off_road_rocks": "Rock Crawl",
                     "off_road_sport_auto": "Rally", "off_road_sport_drift": "Drift"]
        return names[mode] ?? mode.replacingOccurrences(of: "_", with: " ").capitalized
    }

    /// For VoiceOver: the slot as sentences, from the figures it shows (units and symbols as words).
    var spoken: String {
        switch content {
        case .parked(_, _, let label, let weather):
            var parts = [label.map { "Map of where the truck is, at \($0)." } ?? "Map of where the truck is."]
            for st in sleep { parts.append("\(st.label), \(SpokenText.words(st.value)).") }
            if let w = weather {
                parts.append("\(SpokenText.words(w.temp)).")
                if let c = w.condition { parts.append("\(c).") }
                if let wind = w.wind { parts.append("\(SpokenText.words(wind)).") }
            }
            return parts.joined(separator: " ")
        case .figures(let big, let unit, let sub, let small, let stats):
            var parts = ["\(SpokenText.words("\(big) \(unit)"))."]
            parts.append(contentsOf: chips.map { "\(SpokenText.words($0.text))." })
            if let sub { parts.append("\(SpokenText.words(sub)).") }
            if let small { parts.append("\(SpokenText.words(small)).") }
            for st in stats {
                parts.append("\(st.label), \(st.estimated && st.label != "Cost so far" ? "estimated " : "")\(SpokenText.words(st.value))\(st.note.map { ", \($0)" } ?? "").")
            }
            return parts.joined(separator: " ")
        }
    }
}
