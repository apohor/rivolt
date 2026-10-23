import ActivityKit
import Foundation

/// The charging Live Activity. What Apple's push service carries for it is exactly
/// the three fields of `ContentState` (DA's ruling): the charge level as a whole
/// number, the power with one decimal and the minutes to the charge limit. No
/// place, charger, truck name or VIN, and no text. The charge limit itself is fixed
/// when the app starts the activity, so it travels in the attributes, not in updates.
struct ChargingActivityAttributes: ActivityAttributes {
    struct ContentState: Codable, Hashable {
        var socPct: Int
        var powerKw: Double
        /// Nil when it can't be worked out (no power, or already at the limit). The server sends 0 for the same thing.
        var minutesToTarget: Int?

        /// ActivityKit decodes a push's `content-state` with a plain JSONDecoder (no key conversion), so
        /// the server's key names are spelled out here. Any other key in the push is ignored; a renamed
        /// one makes iOS drop the update.
        enum CodingKeys: String, CodingKey {
            case socPct = "soc_pct"
            case powerKw = "power_kw"
            case minutesToTarget = "minutes_to_target"
        }
    }

    /// The charge limit (percent) when the activity started, if the truck reported one.
    var targetPct: Int?
    /// A test from "Send test Live Activity" (fixed numbers): the card says so. Local only, like the rest of the attributes.
    var isTest = false
}

enum ChargingActivityMath {
    /// Minutes until `targetPct` at the current power, from the pack size. Nil when any of it is unknown
    /// or the charge is already at the limit; never a guess from nothing.
    static func minutesToTarget(socPct: Double, targetPct: Double?, packKwh: Double?, powerKw: Double?) -> Int? {
        guard let target = targetPct, target > socPct, let pack = packKwh, pack > 0, let kw = powerKw, kw > 0.1 else { return nil }
        let kwh = (target - socPct) / 100 * pack
        return max(1, Int((kwh / kw * 60).rounded(.up)))
    }

    /// What to show now, from the Now card's data. Nil without a charge level.
    static func state(from summary: NowSummary) -> ChargingActivityAttributes.ContentState? {
        guard let soc = summary.socPct else { return nil }
        let kw = summary.chargePowerKw ?? 0
        return .init(
            socPct: Int(soc.rounded()),
            powerKw: (kw * 10).rounded() / 10,
            minutesToTarget: summary.chargeEta?.minutes
        )
    }

    static func isCharging(_ summary: NowSummary) -> Bool { summary.stateLabel == "Charging" }
}
