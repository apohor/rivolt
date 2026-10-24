import Foundation

/// `GET /api/activity`: the drives and charges of a period, newest first, with no coordinates, routes, place names or
/// cluster geometry. Round trips are already merged by the server.
struct ActivityResponse: Decodable, Equatable {
    let drives: [ActivityDrive]
    let charges: [ActivityCharge]
}

struct ActivityDrive: Decodable, Hashable, Identifiable {
    let id: String
    let vehicleId: String
    let startedAt: Date
    let endedAt: Date
    let distanceMi: Double
    let energyUsedKwh: Double
    let startSocPct: Double
    let endSocPct: Double
    let avgSpeedMph: Double
    let maxSpeedMph: Double
    /// Absent when there is no rate to price the drive with.
    let estimatedCost: Double?
    let estimatedCurrency: String?
    /// The rate the estimate used (per kWh), when there was one.
    let estimatedPricePerKwh: Double?
    /// Only on a merged round trip.
    let roundTripLegs: Int?
    let drivingSeconds: Double?
    /// Whether a trailer was connected: the server's effective value (the user's correction, else what the truck
    /// reported), nil when never known. The connector, not the load. The app does not correct it (web only).
    var trailer: Bool? = nil

    /// A drive with a trailer is left out of the efficiency figures.
    var hasTrailer: Bool { trailer == true }
}

struct ActivityCharge: Decodable, Hashable, Identifiable {
    let id: String
    let vehicleId: String
    let startedAt: Date
    let endedAt: Date
    let energyAddedKwh: Double
    let startSocPct: Double
    let endSocPct: Double
    let maxPowerKw: Double
    let cost: Double
    let currency: String
    /// The ledger's classification: rivian, home_rate, network_estimate, observed_estimate, user,
    /// home_rate_misapplied or unknown. Empty on a server that predates it.
    let costSourceEffective: String?
    let costSourceDerived: Bool?
    let estimatedCost: Double?
    let estimatedCurrency: String?
    /// "dcfc", "ac" or empty; with `place` ("home", "away" or empty) it gives the Home, Away or Fast label.
    let kind: String?
    let place: String?
    let network: String?
    /// The energy was lost (a bug before the fix) and worked out afterwards from the battery's change in charge level: the kWh, and the
    /// cost that follows from it, are estimates. Absent (nil) on a server that predates it.
    var energyEstimated: Bool? = nil
    var isEnergyEstimated: Bool { energyEstimated == true }
}
