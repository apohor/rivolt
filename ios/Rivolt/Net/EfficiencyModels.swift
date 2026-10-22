import Foundation

/// Activity → Efficiency, from `GET /api/insights/efficiency` (the web's EfficiencyView): efficiency over the period
/// and by outside temperature, speed and drive mode, with labels and values already in the requested units.
/// Aggregates only (no coordinates, places, ids or times). Held in memory while the view is up (EfficiencyModel).
struct EfficiencyInsights: Decodable, Equatable {
    /// "imperial" or "metric", as asked.
    let units: String
    /// "mi" or "km".
    let distanceUnit: String
    /// "mi/kWh" or "km/kWh".
    let efficiencyUnit: String
    /// Under this distance a bucket's figure is a hint (`notEnoughData`).
    let minBucketDistance: Double
    let total: EfficiencyBucket
    let byTemperature: [EfficiencyBucket]
    let bySpeed: [EfficiencyBucket]
    let byDriveMode: [EfficiencyBucket]
    let excluded: Excluded?

    struct Excluded: Decodable, Equatable {
        let noEnergy: Int?
        let duplicates: Int?
        let noTemperature: Int?
        let noDriveMode: Int?
    }
}

/// One bucket: its drives' distance, energy and efficiency. `efficiency` is nil only when it has no drives;
/// `notEnoughData` marks one under the minimum distance.
struct EfficiencyBucket: Decodable, Equatable, Identifiable {
    let key: String
    let label: String
    let distance: Double
    let energyKwh: Double
    let efficiency: Double?
    let drives: Int
    let notEnoughData: Bool

    var id: String { key }
}
