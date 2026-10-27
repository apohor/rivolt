import Foundation

/// One window of the off-peak schedule: the days it covers, a start and an end (24-hour "HH:mm"; an end before the start runs past
/// midnight) and the rate per kWh in the account's currency.
struct OffpeakWindow: Codable, Equatable {
    var days: String
    var start: String
    var end: String
    var rate: Double
    var label: String?
}

/// `GET /api/settings/charging/offpeak`: the schedule plus what sits next to it (the home rate that prices every hour outside the
/// windows, the currency of all rates, and the "my electricity is one flat rate" choice).
struct OffpeakSchedule: Decodable, Equatable {
    var tz: String
    var windows: [OffpeakWindow]
    var configured: Bool?
    var baseRate: Double?
    var currency: String?
    var flatRate: Bool?
}

/// The body of `PUT /api/settings/charging/offpeak`: the whole schedule, nothing else (the server refuses a field it doesn't know).
struct OffpeakPut: Encodable, Equatable {
    var tz: String
    var windows: [OffpeakWindow]
}

/// The body and the answer of `PUT /api/settings/charging/flat-rate`.
struct FlatRateBody: Codable, Equatable {
    var flatRate: Bool
}

/// One row of the price book from `GET /api/settings/charging/networks`. The same shape goes back on `PUT`, which replaces the list.
struct ChargingNetwork: Codable, Equatable {
    var name: String
    var pricePerKwh: Double
    var currency: String
    var memberPricePerKwh: Double?
    var memberActive: Bool?
    var memberPlan: String?
    /// Empty for a network of the person's own; set for a built-in one.
    var slug: String?
    var preferred: Bool?
    /// Computed by the server on read: the person has not entered a rate for this built-in network.
    var seeded: Bool?
    var lat: Double?
    var lon: Double?
}

/// One membership plan as a network lists it, from `GET /api/settings/charging/membership-terms`.
struct MembershipTerm: Decodable, Equatable {
    var networkSlug: String
    var network: String
    var plan: String
    var terms: String
    var sourceUrl: String?
    var readVia: String?
    var checked: String?
    /// The exact line to show under the row.
    var label: String
}

struct MembershipTermsResponse: Decodable, Equatable {
    var terms: [MembershipTerm]?
}

/// `GET`/`PUT /api/settings/planner`: the trip planner's saved defaults. `hasAdapter` is absent when not specified.
struct PlannerPrefs: Codable, Equatable {
    var driveMode: String
    var hasAdapter: Bool?
}
