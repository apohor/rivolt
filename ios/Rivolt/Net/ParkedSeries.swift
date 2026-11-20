import Foundation

/// `GET /api/vehicles/{id}/parked-series?tz=`: the last 24 hours of stored readings for the Now card's "Battery while parked" graph
/// (design_preview key now_parked_graph). The server works every figure and the projection out, so the web and the phone say the same
/// numbers; the phone only formats. Sparse data answers empty arrays and null figures. A sleep never overlaps a drive or a charge.
struct ParkedSeries: Decodable, Equatable {
    struct Point: Decodable, Equatable {
        let t: Date
        let soc: Double
        /// Readings of one unbroken parked run share a number; a drive or a charge starts the next.
        let seg: Int
    }

    struct Sleep: Decodable, Equatable {
        let from: Date
        let to: Date
        let open: Bool
    }

    struct Wake: Decodable, Equatable {
        let t: Date
        let socBefore: Double
        let socAfter: Double
    }

    struct Figures: Decodable, Equatable {
        let asleepTodayS: Double
        let wakeUps: Int
        let wakeCostPct: Double?
        let idlePctPerDay: Double?
        let idleMiPerDay: Double?
    }

    struct Projection: Decodable, Equatable {
        let at: Date
        let soc: Double
    }

    let version: Int
    let from: Date
    let to: Date
    /// When the current parked run began; nil while the truck is not parked.
    let parkedSince: Date?
    let parked: Bool
    let changePct: Double?
    let points: [Point]
    let sleeps: [Sleep]
    let wakes: [Wake]
    let figures: Figures?
    let projection: Projection?
}

protocol ParkedSeriesSource: Sendable {
    func parkedSeries(vehicleID: String, tz: String) async throws -> ParkedSeries?
}
