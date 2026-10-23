import Foundation

// The Truck tab's reads, the same ones the web's Truck page makes. None of them carries a coordinate, a place or an
// address; ids in a response that the phone doesn't need (a pack-health sample's charge and vehicle) are not decoded.
// Fields a newer server added are optional, so an older build still decodes.

/// `GET /api/vehicles/{id}/drain`: battery lost while parked and not charging, over 7 and 30 days, and per day.
struct DrainSnapshot: Decodable, Equatable {
    struct Windows: Decodable, Equatable {
        let week: DrainWindow
        let month: DrainWindow

        private enum CodingKeys: String, CodingKey { case week = "7d", month = "30d" }
    }

    let packKwh: Double?
    let windows: Windows
    let daily: [DrainDay]
}

struct DrainWindow: Decodable, Equatable {
    struct Excluded: Decodable, Equatable {
        let insufficient: Int?
        let charging: Int?
        let driving: Int?
    }

    let drainPctPerDay: Double?
    let drainKwhPerDay: Double?
    let sleeps: Int
    let quickRewakes: Int
    let medianSleepSeconds: Double?
    let parkedHours: Double
    let daysWithData: Int
    let daysExcluded: Excluded?
    let sufficient: Bool
    /// When the parked loss happened, asleep or awake. Absent on a server that predates it.
    let split: DrainSplit?
}

/// How the parked loss divides between asleep and awake. An estimate: the battery reports whole percents. It says
/// when, never why.
struct DrainSplit: Decodable, Equatable {
    let sufficient: Bool
    let reason: String?
    let awakeHours: Double?
    let asleepPointsLost: Double?
    let awakePointsLost: Double?
    let awakeShareOfLoss: Double?
}

struct DrainDay: Decodable, Equatable {
    /// "2026-10-21", a local day.
    let day: String
    let drainPct: Double?
    /// "charging", "driving" or "insufficient" when the day is left out.
    let excluded: String?
}

/// `GET /api/vehicles/{id}/pack-health`: the headline and the samples' figures (not their charge or vehicle ids).
struct PackHealth: Decodable, Equatable {
    struct Sample: Decodable, Equatable {
        let at: Date
        let packKwhEffective: Double
        let derateActive: Bool
    }

    struct Headline: Decodable, Equatable {
        let effectiveKwh: Double
        let documentedKwh: Double?
        let reportedKwh: Double?
        let sampleCount: Int
        let window: Int
    }

    let samples: [Sample]?
    let headline: Headline
}

/// `GET /api/vehicles/{id}/trends?units=`: efficiency by month and the usable-capacity estimate from charges.
struct TruckTrends: Decodable, Equatable {
    struct Month: Decodable, Equatable {
        /// "2026-10" in the owner's zone.
        let month: String
        let distance: Double
        let efficiency: Double?
        /// The month's drives with a trailer on their own; absent when there were none.
        var trailer: Trailer? = nil
    }

    struct Trailer: Decodable, Equatable {
        let drives: Int
        let distance: Double
        let energyKwh: Double
        let efficiency: Double?
    }

    struct Capacity: Decodable, Equatable {
        struct Sample: Decodable, Equatable {
            let at: Date
            let kwhEffective: Double
            /// What the charge added (it weights the dot); absent on servers that don't send it yet.
            var energyKwh: Double? = nil
            var socDeltaPct: Double? = nil
        }

        let samples: [Sample]
        let medianKwh: Double?
        let specKwh: Double?
        let minSocDeltaPct: Double?
        let qualifyingCount: Int
        let spanDays: Double
        let minSamples: Int
        let minSpanDays: Double
    }

    let distanceUnit: String
    let efficiencyUnit: String
    let minMonthDistance: Double
    let minMonths: Int
    let qualifyingMonths: Int
    let efficiency: [Month]
    /// The drives over the window left out of those months because a trailer was connected.
    var excludedTrailerDrives: Int? = nil
    /// Nil on a truck whose capacity can't be tracked (the R2).
    let capacity: Capacity?
}

/// `GET /api/vehicles/{id}/cold-range?temp_c&grid&units`: the owner's own efficiency and range near a temperature.
struct ColdRange: Decodable, Equatable {
    struct BasedOn: Decodable, Equatable {
        let drives: Int
        let miles: Double
        let tempLoC: Double
        let tempHiC: Double
    }

    let distanceUnit: String
    let efficiencyUnit: String
    let tempC: Double
    /// "not_enough_data" or "not_tracked" without a figure.
    let reason: String?
    let efficiencyLow: Double?
    let efficiencyHigh: Double?
    let rangeLow: Double?
    let rangeHigh: Double?
    let basedOn: BasedOn?
    /// The grid temperatures (°C) with enough history.
    let available: [Double]
    let minDrives: Int
    let minMiles: Double
}

/// `GET /api/vehicles/{id}/lease`: the terms (set on the web).
struct LeaseTerms: Decodable, Equatable {
    let endDate: String
    let allowedMilesTotal: Double
    let currency: String
}

/// `GET /api/vehicles/{id}/lease/status`: where the lease stands today. Without a recent odometer reading the used
/// and projected figures are null.
struct LeaseStatus: Decodable, Equatable {
    let odometerAt: String?
    let usedMi: Double?
    let allowedToDateMi: Double
    let aheadBehindMi: Double?
    let daysLeft: Int
    let paceMiPerDay: Double?
    let paceBasis: String?
    let projectedTotalMi: Double?
    let projectedReturnOdometerMi: Double?
    let projectedOverageMi: Double?
    let projectedOverageCost: Double?
    let milesPerDayBudget: Double?
}
