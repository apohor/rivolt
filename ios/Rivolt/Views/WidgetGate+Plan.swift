import Foundation

/// The app's half of the widget gate (the widget extension has no plan logic): what /me says about the plan, written to the App Group.
extension WidgetGate {
    /// What the account's plan means for the widgets. `nil` (the server sent no plan fields) means the app does not know: leave the answer as it
    /// was. Billing off, the demo and Pro are not locked.
    static func lockedBy(_ me: AuthUser) -> Bool? {
        guard me.plan != nil else { return nil }
        switch PlanLogic.state(me) {
        case .free, .lapsed: return true
        default: return false
        }
    }

    /// Records the plan for the widgets; true when the answer changed (so the timelines need a reload).
    @discardableResult
    static func record(_ me: AuthUser, store: WidgetStore = WidgetStore()) -> Bool {
        guard let locked = lockedBy(me), locked != store.proLocked else { return false }
        store.setProLocked(locked)
        return true
    }
}
