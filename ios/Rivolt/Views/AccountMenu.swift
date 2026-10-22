import SwiftUI

/// The account menu at the end of every screen's top bar: Refresh (when the screen has something to refresh),
/// Notifications (when this server can push), Acknowledgements and Sign out. It opens its own sheets, so each screen
/// only places it. The screens it opens are the architect's (Settings); the menu itself is shared.
struct AccountMenu: View {
    @Environment(AppState.self) private var state
    /// The truck the notification settings are for (the Live Activity switch is per truck).
    var vehicleID: String?
    /// Nil when the screen has nothing to refresh by hand.
    var onRefresh: (() -> Void)?

    @State private var showNotifications = false
    @State private var showAcknowledgements = false

    var body: some View {
        Menu {
            if let onRefresh {
                Button("Refresh", action: onRefresh)
            }
            if state.notifications.offered {
                Button("Notifications") { showNotifications = true }
            }
            Button("Acknowledgements") { showAcknowledgements = true }
            Divider()
            Button("Sign out", role: .destructive) {
                Task { await state.logout() }
            }
        } label: {
            Image(systemName: "person.crop.circle").font(.title3).foregroundStyle(Theme.textSecondary)
        }
        .accessibilityLabel("Account")
        .sheet(isPresented: $showAcknowledgements) { AcknowledgementsView() }
        .sheet(isPresented: $showNotifications) {
            NotificationSettingsView(manager: state.notifications, liveActivitiesAllowed: state.liveActivity.allowedInSettings,
                                     live: state.liveActivity, vehicleID: vehicleID)
        }
    }
}
