import Foundation

/// App Store screenshots (`-rivolt-store-shot <name>` as a launch argument, debug builds only): the app opens the demo truck on
/// the screen asked for, with the real server's sample data, so the shots are of the real screens. In a release build there is
/// no such argument: `name` is always nil.
enum StoreShots {
    /// now, money, drive, charge or truck.
    static var name: String? {
        #if DEBUG
        UserDefaults.standard.string(forKey: "rivolt-store-shot")
        #else
        nil
        #endif
    }

    /// The tab to start on.
    static var tab: String {
        switch name {
        case "money": return "money"
        case "drive", "charge": return "activity"
        case "truck": return "truck"
        default: return "now"
        }
    }

    /// The Activity row to open: "drive" or "charge".
    static var open: String? { name == "drive" || name == "charge" ? name : nil }
}
