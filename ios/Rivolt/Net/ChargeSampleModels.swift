import Foundation

/// One telemetry sample of a charge's window from `GET /api/samples`, only what the charging curve draws: when, the
/// battery level, the charger's power and the pack's cell temperatures. The position, speed, range, odometer, the
/// other temperatures and the rest of the row are not decoded. Held in memory while the charge's screen is open.
struct ChargeSample: Decodable, Equatable {
    let at: Date
    let batteryLevelPct: Double
    let chargerPowerKw: Double
    /// Absent on rows recorded while the truck slept, and on older ones.
    let packTempAvgC: Double?
    let packTempMinC: Double?
    let packTempMaxC: Double?

    private enum CodingKeys: String, CodingKey {
        case at = "At", batteryLevelPct = "BatteryLevelPct", chargerPowerKw = "ChargerPowerKW"
        case packTempAvgC = "pack_temp_avg_c", packTempMinC = "pack_temp_min_c", packTempMaxC = "pack_temp_max_c"
    }
}
