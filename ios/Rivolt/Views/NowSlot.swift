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

    let content: Content

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
                     fix: (lat: Double, lon: Double)?, placeLabel: String?, clock: Date, calendar: Calendar = .current) -> NowSlotModel? {
        switch summary.kind {
        case .charging:
            guard let detail = ChargingDetail.make(summary: summary, session: session, charge: charge), let kw = detail.powerKw else { return nil }
            return NowSlotModel(content: charging(summary: summary, detail: detail, kw: kw, snapshot: snapshot, clock: clock))
        case .driving:
            guard let kph = summary.speedKph else { return nil }
            return NowSlotModel(content: driving(kph: kph, drive: drive(snapshot: snapshot, live: liveDrive)))
        case .parked, .plugged:
            guard let fix else { return nil }
            let hour = calendar.component(.hour, from: clock)
            let weather = snapshot?.ambient.map { weatherView($0, day: hour >= 6 && hour < 20) }
            return NowSlotModel(content: .parked(lat: fix.lat, lon: fix.lon, label: placeLabel, weather: weather))
        default:
            return nil
        }
    }

    // MARK: Charging

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
        if let km = detail.rangeAddedKm, km > 0 { stats.append(Stat(label: "Range added", value: "+\(NowFormat.distance(mi: km / 1.609344))", note: nil)) }
        if let rate = detail.rateKmPerHour, rate > 0 { stats.append(Stat(label: "Rate", value: "\(NowFormat.distance(mi: rate / 1.609344))/h", note: nil)) }
        let started = summary.chargeStartedAt ?? snapshot?.charge?.startedAt
        if let elapsed = detail.elapsed {
            stats.append(Stat(label: "Elapsed", value: NowFormat.duration(elapsed), note: nil))
        } else if let started, clock > started {
            stats.append(Stat(label: "Elapsed", value: NowFormat.duration(clock.timeIntervalSince(started)), note: nil))
        }
        if let cost = detail.cost {
            stats.append(Stat(label: "Cost so far", value: NowFormat.costText(cost), note: nil))
        }
        if let started { stats.append(Stat(label: "Plugged in", value: started.formatted(date: .omitted, time: .shortened), note: nil)) }
        return .figures(big: NumberText.fixed(kw, kw < 10 ? 1 : 0), unit: "kW", sub: sub, small: small, stats: stats)
    }

    // MARK: Driving

    static func drive(snapshot: NowSnapshot?, live: LiveDrive?) -> Drive? {
        if let d = snapshot?.drive {
            return Drive(distanceMi: d.distanceMi, elapsedSec: d.durationS, energyKwh: d.energyKwh, miPerKwh: d.efficiencyMiPerKwh)
        }
        guard let d = live, d.distanceMi > 0 else { return nil }
        let eff = d.distanceMi >= 1 && d.energyUsedKwh > 0 && d.miPerKwh > 0 ? d.miPerKwh : nil
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

    /// For VoiceOver: the parked slot's own words (the charging and driving figures are already in the card's).
    var spoken: String? {
        guard case .parked(_, _, let label, let weather) = content else { return nil }
        var parts = [label.map { "Map of where the truck is, at \($0)." } ?? "Map of where the truck is."]
        if let w = weather {
            parts.append("\(SpokenText.words(w.temp)).")
            if let c = w.condition { parts.append("\(c).") }
            if let wind = w.wind { parts.append("\(wind).") }
            if let p = w.precip { parts.append("\(p).") }
        }
        return parts.joined(separator: " ")
    }
}
