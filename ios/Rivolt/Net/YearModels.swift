import Foundation

/// `GET /api/year-in-rivolt`: the signed-in user's own calendar year (UTC), computed on the server. An ineligible answer carries only
/// `eligible`, `year` and `reason`. Months are "2026-07"; nothing here names a place, a day, a time, a VIN or the truck.
struct YearInRivolt: Decodable, Equatable {
    struct Shares: Decodable, Equatable {
        let home: Double
        let away: Double
        let fast: Double
    }
    struct MonthMiles: Decodable, Equatable {
        let miles: Double
        let month: String
    }
    struct LongDrive: Decodable, Equatable {
        let miles: Double
        let miPerKwh: Double
        let month: String
    }
    struct Coldest: Decodable, Equatable {
        let tempF: Double
        let month: String
    }
    struct Cost: Decodable, Equatable {
        let amount: Double
        let currency: String
        let estimated: Bool
    }

    let eligible: Bool
    let year: Int
    let reason: String?
    let sinceMonth: String?
    let driveCount: Int?
    let miles: Double?
    /// The energy the drives used, not energy charged.
    let kwh: Double?
    let chargeCount: Int?
    /// Fractions 0–1 of the energy charged.
    let energyShares: Shares?
    let efficiencyMiPerKwh: Double?
    let efficiencyEstimated: Bool?
    let bestEfficiencyMiPerKwh: Double?
    let longestTrip: MonthMiles?
    let mostEfficientLongDrive: LongDrive?
    let coldestDrive: Coldest?
    let plannedTrips: Int?
    let plannedStops: Int?
    let model: String?
    let cost: Cost?
    let costPerMile: Double?
}
