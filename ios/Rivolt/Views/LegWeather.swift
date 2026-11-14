import Foundation

/// What a leg's weather says, as facts, in the account's units: the same rules and words as the web's planner (web/src/plan/legWeather.ts). The map label
/// and the stop row show these; the energy effect is only in the stop's detail, worded as an estimate. Rain shows only when the server applied its wet
/// factor (precipitation over 0.5 mm); snow only when the server says so; the wind only when it is worth telling a driver.
enum LegWeather {
    struct Facts: Equatable {
        /// "41 °F" (a tag) and "41°" (the map label); nil when the leg has no temperature.
        var temp: String?
        var tempShort: String?
        /// "37 °F" when the feels-like differs from the air temperature; said in the leg's details, not in the tags.
        var feels: String?
        /// Along the direction of travel: a headwind blows against it, a tailwind with it.
        var wind: Wind?
        var rain = false
        var snow = false

        struct Wind: Equatable {
            enum Kind: String { case head, tail }
            var kind: Kind
            var speed: Int
            var unit: String
        }
    }

    static let wetMm = 0.5
    /// A headwind or tailwind under this (km/h) is noise.
    static let windMinKph = 8.0
    static let kphPerMph = 1.609344

    private static func rounded(_ c: Double, celsius: Bool) -> Int { Int((celsius ? c : c * 1.8 + 32).rounded()) }
    /// A real minus, not a hyphen.
    private static func signed(_ n: Int) -> String { (n < 0 ? "−" : "") + String(abs(n)) }

    static func facts(_ leg: TripWeatherLeg?, celsius: Bool, metric: Bool) -> Facts? {
        guard let leg else { return nil }
        var out = Facts(snow: leg.snow == true)
        let unit = celsius ? "C" : "F"
        // The air temperature is what the truck shows; the feels-like stands in until the server sends it.
        if let air = leg.airTempC ?? leg.tempC, air.isFinite {
            let t = rounded(air, celsius: celsius)
            out.temp = "\(signed(t)) °\(unit)"
            out.tempShort = "\(signed(t))°"
            if leg.airTempC != nil, let f = leg.tempC, f.isFinite {
                let r = rounded(f, celsius: celsius)
                if r != t { out.feels = "\(signed(r)) °\(unit)" }
            }
        }
        if let hw = leg.headwindKph, hw.isFinite, abs(hw) >= windMinKph {
            out.wind = Facts.Wind(kind: hw > 0 ? .head : .tail,
                                  speed: Int((metric ? abs(hw) : abs(hw) / kphPerMph).rounded()), unit: metric ? "km/h" : "mph")
        }
        out.rain = !out.snow && (leg.precipMm ?? 0) > wetMm
        if out.temp == nil, out.wind == nil, !out.rain, !out.snow { return nil }
        return out
    }

    /// The stop row's tags: "41 °F", "25 mph headwind", "rain" or "snow expected".
    static func tags(_ f: Facts?) -> [String] {
        guard let f else { return [] }
        var out: [String] = []
        if let t = f.temp { out.append(t) }
        if let w = f.wind { out.append("\(w.speed) \(w.unit) \(w.kind.rawValue)wind") }
        if f.snow { out.append("snow expected") } else if f.rain { out.append("rain") }
        return out
    }

    /// The map label: "41° ↙ 25 mph rain" or "21° snow expected". The arrow points where the air moves relative to the way you drive (the stop row's
    /// tags say it in words).
    static func mapLabel(_ f: Facts?) -> String? {
        guard let f else { return nil }
        var parts: [String] = []
        if let t = f.tempShort { parts.append(t) }
        if f.snow {
            parts.append("snow expected")
        } else {
            if let w = f.wind { parts.append("\(w.kind == .head ? "↙" : "↗") \(w.speed) \(w.unit)") }
            if f.rain { parts.append("rain") }
        }
        return parts.isEmpty ? nil : parts.joined(separator: " ")
    }

    /// The one estimate line (the stop's detail): the server's per-leg multiplier, "Weather and your setup" when the plan used a driver profile. Under 1 %
    /// either way it says nothing.
    static func estimateLine(multiplier: Double, withSetup: Bool) -> String? {
        let pct = Int(((multiplier - 1) * 100).rounded())
        guard pct != 0 else { return nil }
        return "\(withSetup ? "Weather and your setup" : "Weather"): \(pct < 0 ? "−" : "+")\(abs(pct))% (estimate)"
    }

    /// "Climb +1,900 ft · descent −2,200 ft", in the account's unit, once the server sends both.
    static func climbLine(climbM: Double?, descentM: Double?, metric: Bool) -> String? {
        guard let climbM, let descentM else { return nil }
        func f(_ m: Double) -> String {
            let n = NumberFormatter()
            n.numberStyle = .decimal
            n.locale = Locale(identifier: "en_US")
            n.maximumFractionDigits = 0
            return n.string(from: NSNumber(value: (metric ? m : m * 3.28084).rounded())) ?? ""
        }
        let u = metric ? "m" : "ft"
        return "Climb +\(f(climbM)) \(u) · descent −\(f(descentM)) \(u)"
    }

    /// Whether any leg shows a climb line, so the plan carries the terrain credit once.
    static func showsClimb(_ legs: [TripWeatherLeg]?) -> Bool {
        (legs ?? []).contains { climbLine(climbM: $0.climbM, descentM: $0.descentM, metric: true) != nil }
    }

    /// The terrain data asks to be credited wherever the climb shows (the web's line).
    static let terrainCredit = "Terrain: USGS 3DEP, GMTED2010 and SRTM; Natural Resources Canada (Open Government Licence – Canada); INEGI; ArcticDEM; NOAA ETOPO1"
}
