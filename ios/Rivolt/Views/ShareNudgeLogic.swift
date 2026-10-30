import Foundation

/// Money's one-time nudge to share the statement card, the web's twin (growth's words, DA-checked): shown once to an account with a
/// month to share, gone for good after "Not now" or once the share sheet has been opened from anywhere. The card is the masked one
/// "Share statement" makes (no places or addresses, cost only if added; nothing is posted). No new data, no events.
enum ShareNudgeLogic {
    static let dismissedKey = "rivolt.shareNudge.dismissed"
    static let title = "Share what charging cost this month"
    static let body = "Your statement card shows the month's kWh and miles, with cost left out unless you add it. Nothing is posted; you pick where it goes."
    static let shareButton = "Share statement"
    static let laterButton = "Not now"

    /// Whether the card is drawn: something to share (not an empty month), the share action available, not in the demo, not dismissed.
    static func shows(empty: Bool, canShare: Bool, demo: Bool, dismissed: Bool) -> Bool {
        !empty && canShare && !demo && !dismissed
    }
}
