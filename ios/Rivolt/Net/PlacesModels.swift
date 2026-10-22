import Foundation

/// `GET /api/charge-split`: how many charges, and how much energy, were Home, Away and Fast in a period. Counts and
/// totals only: no coordinates, places or addresses. A charge with no label is in no bucket and not in `total`.
struct ChargeSplit: Decodable, Equatable {
    struct Bucket: Decodable, Equatable {
        let sessions: Int
        let energyKwh: Double
    }

    let home: Bucket
    let away: Bucket
    let fast: Bucket
    let total: Int
}
