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
}

/// One row of `GET /api/sleep-activity`: a UTC day, in hours.
struct DayActivity: Decodable, Equatable {
    let day: Date
    let asleepH: Double
    let idleAwakeH: Double
    let sleeps: Int?
    let failedSleeps: Int?
}
