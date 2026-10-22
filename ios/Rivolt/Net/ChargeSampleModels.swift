import Foundation

/// One telemetry sample of a charge's window from `GET /api/samples`, only what the charge's screen uses: when, the
/// battery level, the charger's power, the pack's cell temperatures for the curve, and the charge limit and the
/// outside and inside temperatures for the session card. The position, speed, range, odometer and the rest of the row
/// are not decoded. Held in memory while the charge's screen is open.
struct ChargeSample: Decodable, Equatable {
    let at: Date
    let batteryLevelPct: Double
    let chargerPowerKw: Double
    /// Absent on rows recorded while the truck slept, and on older ones.
    let packTempAvgC: Double?
    let packTempMinC: Double?
    let packTempMaxC: Double?
    /// The limit the truck was set to at that moment (0 when not reported), and the two temperatures (0 when not).
    var chargeLimitPct: Double? = nil
    var outsideTempC: Double? = nil
    var insideTempC: Double? = nil

    /// The app's decoder turns `pack_temp_avg_c` into `packTempAvgC` before it looks the key up, so those three are
    /// named as it leaves them; the others are the server's Go field names.
    private enum CodingKeys: String, CodingKey {
        case at = "At", batteryLevelPct = "BatteryLevelPct", chargerPowerKw = "ChargerPowerKW"
        case packTempAvgC, packTempMinC, packTempMaxC
        case chargeLimitPct = "ChargeLimitPct", outsideTempC = "OutsideTempC", insideTempC = "InsideTempC"
    }
}
