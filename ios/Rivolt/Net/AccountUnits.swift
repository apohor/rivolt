import Foundation

/// `GET /api/settings/units` and the `units` in `GET /api/auth/me`: how the account shows distance and temperature (the server
/// always stores km and °C). A distance source of "unset" means nothing was chosen yet and the values are placeholders.
struct AccountUnits: Decodable, Equatable {
    let distanceUnit: String
    let temperatureUnit: String
    /// "user", "region" or "unset".
    let distanceSource: String
    /// "user" or "follows_distance".
    let temperatureSource: String

    var isUnset: Bool { distanceSource == "unset" }
    /// The temperature to hand to `DisplayUnits.apply`: nil when it follows the distance.
    var temperatureForDisplay: String? { temperatureSource == "user" ? temperatureUnit : nil }
}

/// `PATCH /api/settings/units`: a pick of either unit ("auto" returns the temperature to following the distance), or the one
/// first-time seed from the phone's region (which only fills an account that has nothing yet).
struct UnitsPatch: Encodable, Equatable {
    var distanceUnit: String?
    var temperatureUnit: String?
    var seed: Bool?
}
