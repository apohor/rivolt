import Foundation

/// The units the app and its widgets show: miles or kilometres, °F or °C. They are the account's (so the web and the phone
/// agree), kept in the App Group's defaults so the widgets read the same; until the account has told the app, and in the demo,
/// the phone's region decides.
enum DisplayUnits {
    private static let distanceKey = "units.distance"
    private static let temperatureKey = "units.temperature"

    private static var defaults: UserDefaults { UserDefaults(suiteName: "group.dev.rivolt.app") ?? .standard }

    /// Kilometres (and km/h, kWh per 100 km) rather than miles.
    static var distanceMetric: Bool {
        switch defaults.string(forKey: distanceKey) {
        case "km": return true
        case "mi": return false
        default: return regionIsMetric
        }
    }

    /// °C rather than °F: the account's pick, else following the distance unit (miles, °F; km, °C).
    static var celsius: Bool {
        switch defaults.string(forKey: temperatureKey) {
        case "c": return true
        case "f": return false
        default: return distanceMetric
        }
    }

    /// The temperature follows the distance unit (nothing was picked for it).
    static var temperatureFollowsDistance: Bool { defaults.string(forKey: temperatureKey) == nil }

    /// The phone's region says miles in the US, Liberia, Myanmar and the UK, kilometres elsewhere.
    static var regionIsMetric: Bool { !milesRegion(Locale.current.region?.identifier) }

    static func milesRegion(_ region: String?) -> Bool { ["US", "LR", "MM", "GB"].contains(region ?? "US") }

    /// What the account says, taken for display: `temperature` nil means it follows the distance.
    static func apply(distance: String, temperature: String?) {
        defaults.set(distance == "km" ? "km" : "mi", forKey: distanceKey)
        if let temperature { defaults.set(temperature == "c" ? "c" : "f", forKey: temperatureKey) } else { defaults.removeObject(forKey: temperatureKey) }
    }

    /// Sign-out or a server switch: the next person starts from the phone's region.
    static func reset() {
        defaults.removeObject(forKey: distanceKey)
        defaults.removeObject(forKey: temperatureKey)
    }
}
