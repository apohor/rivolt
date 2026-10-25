import Foundation

/// `GET /api/settings/home-location`: the saved home, which the home/away label and the home rate use for new charges.
/// `previousSet` (an earlier home can be restored) and `addressSearch` (this server can look up a street address) are read-only.
struct HomeLocation: Decodable, Equatable {
    var set: Bool = false
    var latitude: Double = 0
    var longitude: Double = 0
    var label: String?
    var address: String?
    /// "city", "address", "coordinates", "charge" or empty.
    var source: String?
    var previousSet: Bool?
    var addressSearch: Bool?

    var canRestore: Bool { previousSet == true }
    var canSearchAddress: Bool { addressSearch == true }
}

/// What `PUT /api/settings/home-location` takes: a new home, or `set: false` to clear it.
struct HomeLocationBody: Encodable, Equatable {
    var set: Bool
    var latitude: Double?
    var longitude: Double?
    var label: String?
    var address: String?
    var source: String?

    static let clear = HomeLocationBody(set: false)
}

/// One match from `POST /api/settings/home-location/search` (an address on the server's own geocoder).
struct PlaceMatch: Decodable, Equatable, Identifiable {
    let name: String
    let latitude: Double
    let longitude: Double
    var admin1: String?
    var country: String?

    var id: String { "\(latitude),\(longitude),\(name)" }
    /// "123 Main St, Oregon, United States".
    var line: String { [name, admin1, country].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: ", ") }
}

struct HomeSearchBody: Encodable { let q: String }

/// The per-truck inputs the efficiency and planner figures use (`GET` and `PUT /api/vehicles/{id}/profile`). Every field is
/// optional: an empty profile means nothing is set. `nativeNacs` nil is "decide from the model year".
struct VehicleProfile: Codable, Equatable, Hashable {
    var tireType: String?
    var wheelInches: Int?
    var accessories: [String]?
    var defaultExtraLoadLb: Double?
    var frequentlyTows: Bool?
    var tirePlacardPsi: Double?
    var nativeNacs: Bool?
}

/// The truck's last reported position, for "use the truck's location" (the phone's own location is never read).
struct TruckPosition: Decodable, Equatable {
    var latitude: Double?
    var longitude: Double?
    /// When the truck last had a GPS fix; the server sends the zero time (year 1) when it never has.
    var locationFixAt: Date?

    init(latitude: Double?, longitude: Double?, locationFixAt: Date? = nil) {
        self.latitude = latitude
        self.longitude = longitude
        self.locationFixAt = locationFixAt
    }

    /// Zero is how the truck says "no fix", and so is a fix time that was never set.
    var coordinate: (latitude: Double, longitude: Double)? {
        guard let latitude, let longitude, latitude != 0 || longitude != 0 else { return nil }
        if let at = locationFixAt, at.timeIntervalSince1970 < 946_684_800 { return nil } // before 2000
        return (latitude, longitude)
    }

    /// The time of the fix when it is old enough to be worth saying (an hour or more, e.g. a truck that has been asleep).
    func staleFix(now: Date = .now) -> Date? {
        guard let at = locationFixAt, at.timeIntervalSince1970 >= 946_684_800, now.timeIntervalSince(at) >= 3600 else { return nil }
        return at
    }

    static func == (a: TruckPosition, b: TruckPosition) -> Bool { a.latitude == b.latitude && a.longitude == b.longitude && a.locationFixAt == b.locationFixAt }
}
