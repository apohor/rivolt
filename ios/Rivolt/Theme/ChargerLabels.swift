// Generated from ios/design/charger-labels.json by ios/design/generate_charger_labels.py. Do not edit by hand:
// change web/src/lib/chargerLabels.json, regenerate charger-labels.json, then run the script. ChargerLabelsTests checks the two agree.
import Foundation

/// The charger types, one set of words for the whole app (and the web): Fast (DC), Medium (Level 2), Hotel, Camping, Outlet.
enum ChargerKind: String, CaseIterable, Equatable {
    case fast, medium, hotel, camping, outlet
}

enum ChargerLabels {
    static let all = "All"
    static let infoLabel = "What the charger types mean"

    /// The one-word label.
    static func label(_ kind: ChargerKind) -> String {
        switch kind {
        case .fast: return "Fast"
        case .medium: return "Medium"
        case .hotel: return "Hotel"
        case .camping: return "Camping"
        case .outlet: return "Outlet"
        }
    }

    /// The optional explanation: at most two short sentences, the fact first, then one caveat.
    static func sentence(_ kind: ChargerKind) -> String {
        switch kind {
        case .fast: return "DC fast charging, usually 50 kW and up; a few older stations are slower, and each pin shows its rating. About 100 miles in 15 to 30 minutes at 150 kW or faster, closer to an hour at 50 kW."
        case .medium: return "Level 2 (AC), 7 to 11 kWh per hour. About 15 to 30 miles per hour."
        case .hotel: return "A hotel with a listed Level 2 charger, which may be taken or out of date: check with the hotel. If it's free and working, a night adds 100 miles or more."
        case .camping: return "RV hookup. A 50-amp (14-50) site charges at Level 2 speed with a 240 V portable charger; a 30-amp site is much slower."
        case .outlet: return "Household outlet (120 V). About 3 to 4 miles per hour."
        }
    }
}
