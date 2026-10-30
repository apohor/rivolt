import Foundation

/// Money's one-time nudge toward the off-peak savings card, the web's twin (growth's words, DA-checked): shown to an account that has a
/// home rate but no off-peak hours and has not said its electricity is one flat rate. "Not now" is remembered on this device; it goes by
/// itself once hours are saved. Never in the demo. Setting the hours is a write the app is allowed (PUT off-peak, DA Oct 26).
enum OffpeakPromptLogic {
    static let dismissedKey = "rivolt.offpeakPrompt.dismissed"
    static let title = "Do you have cheaper hours?"
    static let body = "Enter your utility's off-peak windows and rate, and Money shows what moving your home charging into them could have saved, as an estimate from your own charges."
    static let setButton = "Set off-peak hours"
    static let laterButton = "Not now"

    /// From the off-peak answer (`baseRate` is the home rate; 0 means none): a home rate, no hours, no flat-rate choice.
    static func applies(_ s: OffpeakSchedule?) -> Bool {
        guard let s else { return false }
        return (s.baseRate ?? 0) > 0 && s.configured == false && s.flatRate != true
    }

    /// Whether the card is drawn.
    static func shows(_ s: OffpeakSchedule?, dismissed: Bool, demo: Bool) -> Bool {
        !dismissed && !demo && applies(s)
    }
}
