import Foundation

/// `GET /api/chart-points`: what the Home charts need from drives and charges, with no coordinates, ids, costs or
/// places. Both lists are oldest first.
struct ChartPoints: Decodable, Equatable {
    let drives: [DrivePoint]
    let charges: [ChargePoint]
}

struct DrivePoint: Decodable, Equatable {
    let startedAt: Date
    let endedAt: Date
    let distanceMi: Double
    let startSocPct: Double
    let endSocPct: Double
}

struct ChargePoint: Decodable, Equatable {
    let startedAt: Date
    let endedAt: Date
    let startSocPct: Double
    let endSocPct: Double
    /// The limit the truck was set to for this charge (1 to 100); null or 0 when unknown, and absent on a server
    /// that predates it.
    var chargeLimitPct: Double? = nil
    /// "dcfc" for a DC fast charge, "ac" otherwise; absent until the server sends it (the daily chart's bolt needs it).
    var kind: String? = nil
}

/// One row of `GET /api/sleep-activity`: a UTC day, in hours.
struct DayActivity: Decodable, Equatable {
    let day: Date
    let asleepH: Double
    let idleAwakeH: Double
    let sleeps: Int?
    let failedSleeps: Int?
}
