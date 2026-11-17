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
    }

    struct Weather: Equatable {
        let temp: String
        /// "Sunny here now".
        let condition: String?
        /// An SF Symbol name.
        let symbol: String?
        let wind: String?
        let precip: String?
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

    /// The server's drive, or the live drive's figures when the server has none.
    struct Drive: Equatable {
        let distanceMi: Double
        let elapsedSec: Double
        let energyKwh: Double?
        let miPerKwh: Double?
    }

    /// Nil when the state has no slot, so the picture stays: charging without a power reading, driving without a speed, parked without a fix.
    static func make(summary: NowSummary, snapshot: NowSnapshot?, session: LiveSession?, charge: ChargeBrief?, liveDrive: LiveDrive?,
                     fix: (lat: Double, lon: Double)?, home: Home?, gear: String? = nil, mode: String? = nil,
                     clock: Date, calendar: Calendar = .current) -> NowSlotModel? {
        let atHome = home != nil && fix != nil && ChargerPin.distanceM(home!.lat, home!.lon, fix!.lat, fix!.lon) <= homeRadiusM
        switch summary.kind {
        case .charging:
            guard let detail = ChargingDetail.make(summary: summary, session: session, charge: charge), let kw = detail.powerKw else { return nil }
            // As the web: fast at a session peak of 50 kW or more, else the home charger at home, else public only when the peak is known.
            let peak = max(snapshot?.charge?.peakKw ?? 0, kw)
            let kind = peak >= fastPeakKw ? "Fast charger" : atHome ? "Home charger" : snapshot?.charge?.peakKw != nil ? "Public charger" : nil
            return NowSlotModel(content: charging(summary: summary, detail: detail, kw: kw, snapshot: snapshot, clock: clock),
                                chips: kind.map { [Chip(text: $0, accent: true)] } ?? [])
        case .driving:
            guard let kph = summary.speedKph else { return nil }
            var chips: [Chip] = []
            if let gear, !gear.isEmpty { chips.append(Chip(text: gear.uppercased(), accent: false)) }
            if let mode, !mode.isEmpty { chips.append(Chip(text: driveModeWords(mode), accent: true)) }
            return NowSlotModel(content: driving(kph: kph, drive: drive(snapshot: snapshot, live: liveDrive, packKwh: summary.packKwh)), chips: chips)
        case .parked, .plugged:
            guard let fix else { return nil }
            let hour = calendar.component(.hour, from: clock)
            let weather = snapshot?.ambient.map { weatherView($0, day: hour >= 6 && hour < 20) }
            return NowSlotModel(content: .parked(lat: fix.lat, lon: fix.lon, label: atHome ? (home?.label ?? "Home") : nil, weather: weather))
        default:
            return nil
        }
    }

    // MARK: Charging

    private static let costNotes = ["rivian": "billed by Rivian", "user": "your price", "home_rate": "at your home rate",
                                    "network_estimate": "network rate", "observed_estimate": "from similar sessions"]

    private static func charging(summary: NowSummary, detail: ChargingDetail, kw: Double, snapshot: NowSnapshot?, clock: Date) -> Content {
        var sub: String?
        var small: String?
        if let eta = summary.chargeEta {
            let when = clock.addingTimeInterval(TimeInterval(eta.minutes) * 60).formatted(date: .omitted, time: .shortened)
            let limit = summary.chargeTargetPct
            if let limit, limit < 100 { sub = "Reaches \(Int(limit.rounded()))% at \(when)" } else { sub = "Full at \(when)" }
            small = "\(NowFormat.duration(TimeInterval(eta.minutes) * 60)) left\(eta.source == .truck ? "" : " (est.)")"
                + (limit.map { " · target \(Int($0.rounded()))%" } ?? "")
        }
        var stats: [Stat] = []
        if let kwh = detail.energyKwh, kwh > 0 {
            stats.append(Stat(label: "Added", value: "\(detail.energyEstimated ? "≈ " : "")+\(NowFormat.oneDecimal(kwh)) kWh", note: nil))
        }
        // The truck's own figures come from the live session; without one (the demo, a home charge) the battery's gain at today's km per percent, marked ≈.
        let started = summary.chargeStartedAt ?? snapshot?.charge?.startedAt
        let elapsed: TimeInterval? = detail.elapsed ?? started.flatMap { clock > $0 ? clock.timeIntervalSince($0) : nil }
        var rangeKm = detail.rangeAddedKm.flatMap { $0 > 0 ? $0 : nil }
        let estimated = rangeKm == nil
        if estimated, let added = summary.chargeAddedPct, added > 0, let soc = summary.socPct, soc > 0, let range = summary.rangeKm {
            rangeKm = range / soc * added
        }
        let mark = estimated ? "≈ " : ""
        if let km = rangeKm { stats.append(Stat(label: "Range added", value: "\(mark)+\(NowFormat.distance(mi: km / 1.609344))", note: nil)) }
        let truckRate = detail.rateKmPerHour.flatMap { $0 > 0 ? $0 : nil }
        let rate = truckRate ?? rangeKm.flatMap { km in elapsed.flatMap { $0 >= 300 ? km / ($0 / 3600) : nil } }
        if let rate { stats.append(Stat(label: "Rate", value: "\(truckRate == nil ? "≈ " : "")\(NowFormat.distance(mi: rate / 1.609344))/h", note: nil)) }
        if let elapsed { stats.append(Stat(label: "Elapsed", value: NowFormat.duration(elapsed), note: nil)) }
        // The cost so far is the server's (`/now` charge), as the web: ≈ when it is an estimate, with where it comes from.
        if let c = snapshot?.charge, let cost = c.cost {
            let estimate = c.costIsEstimate != false
            stats.append(Stat(label: "Cost so far", value: "\(estimate ? "≈ " : "")\(NowFormat.money(cost, currency: c.currency ?? "USD"))",
                              note: c.costSource.flatMap { costNotes[$0] } ?? (estimate ? "estimate" : nil)))
        }
        if let started { stats.append(Stat(label: "Plugged in", value: started.formatted(date: .omitted, time: .shortened), note: nil)) }
        return .figures(big: NumberText.fixed(kw, kw < 10 ? 1 : 0), unit: "kW", sub: sub, small: small, stats: stats)
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
            if d.distanceMi > 0 { stats.append(Stat(label: "Distance", value: NowFormat.distance(mi: d.distanceMi), note: nil)) }
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

    /// Only what the server sent. By day a clear sky is "Sunny"; the 3 h figure is rain or snow by the current condition. Snow has no amount
    /// (the forecast is water-equivalent, which understates it); small rain amounts read "< 0.1 in" or "< 0.1 mm".
    static func weatherView(_ w: NowSnapshot.Ambient, day: Bool) -> Weather {
        let metric = NowFormat.usesMetric
        let known = w.condition.flatMap { conditions[$0] }
        var precip: String?
        if let mm = w.rainNext3hMm {
            if w.condition == "snow" {
                precip = mm < 0.05 ? "No snow next 3 h" : "Snow next 3 h"
            } else if mm < 0.05 {
                precip = "No rain next 3 h"
            } else {
                let amount: String
                if metric { amount = mm < 0.1 ? "< 0.1 mm" : "\(trimmed(mm)) mm" } else { amount = mm / 25.4 < 0.1 ? "< 0.1 in" : "\(trimmed(mm / 25.4)) in" }
                precip = "Rain next 3 h · \(amount)"
            }
        }
        let label = known.map { w.condition == "clear" && day ? "Sunny" : $0.label }
        let symbol = w.condition == "clear" && !day ? "moon" : known?.symbol
        return Weather(
            temp: NowComfort.wholeDegrees(celsius: w.tempC),
            condition: label.map { "\($0) here now" },
            symbol: symbol,
            wind: w.windKph.map { "Wind \(metric ? "\(Int($0.rounded())) km/h" : "\(Int(($0 / 1.609344).rounded())) mph")" },
            precip: precip)
    }

    /// One decimal, the trailing zero dropped ("3", "0.4").
    private static func trimmed(_ v: Double) -> String {
        let r = (v * 10).rounded() / 10
        return r == r.rounded() ? NumberText.fixed(r, 0) : NumberText.fixed(r, 1)
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
            if let w = weather {
                parts.append("\(SpokenText.words(w.temp)).")
                if let c = w.condition { parts.append("\(c).") }
                if let wind = w.wind { parts.append("\(SpokenText.words(wind)).") }
                if let p = w.precip { parts.append("\(SpokenText.words(p)).") }
            }
            return parts.joined(separator: " ")
        case .figures(let big, let unit, let sub, let small, let stats):
            var parts = ["\(SpokenText.words("\(big) \(unit)"))."]
            parts.append(contentsOf: chips.map { "\(SpokenText.words($0.text))." })
            if let sub { parts.append("\(SpokenText.words(sub)).") }
            if let small { parts.append("\(SpokenText.words(small)).") }
            for st in stats {
                parts.append("\(st.label), \(SpokenText.words(st.value))\(st.note.map { ", \($0)" } ?? "").")
            }
            return parts.joined(separator: " ")
        }
    }
}
