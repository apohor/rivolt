import SwiftUI

/// The account menu at the end of every screen's top bar: Refresh (when the screen has something to refresh),
/// Notifications (when this server can push), Charging rates (the home rate, read and changed), Appearance (System, Light or Dark, saved on this iPhone), Privacy policy and Terms (the pages on the signed-in server, in Safari), Acknowledgements and Sign out. It opens its own sheets, so each screen
/// only places it. The screens it opens are the architect's (Settings); the menu itself is shared.
struct AccountMenu: View {
    @Environment(AppState.self) private var state
    /// The truck the notification settings are for (the Live Activity switch is per truck).
    var vehicleID: String?
    /// Nil when the screen has nothing to refresh by hand.
    var onRefresh: (() -> Void)?

    @AppStorage(AppearanceChoice.storageKey) private var appearance = AppearanceChoice.system
    @State private var showNotifications = false
    @State private var showAcknowledgements = false
    @State private var showRates = false
    @State private var showConnection = false

    var body: some View {
        Menu {
            if let onRefresh {
                Button("Refresh", action: onRefresh)
            }
            if state.notifications.offered {
                Button("Notifications") { showNotifications = true }
            }
            Button("Charging rates") { showRates = true }
            Picker("Appearance", selection: $appearance) {
                ForEach(AppearanceChoice.allCases) { Text($0.title).tag($0) }
            }
            Link("Privacy policy", destination: state.serverURL.appending(path: "privacy"))
            Link("Terms", destination: state.serverURL.appending(path: "terms"))
            Button("Connection details") { showConnection = true }
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
        .sheet(isPresented: $showRates) { ChargingRatesView() }
        .sheet(isPresented: $showConnection) { ConnectionDetailsView(diagnostics: state.connection, serverURL: state.serverURL) }
        .sheet(isPresented: $showNotifications) {
            NotificationSettingsView(manager: state.notifications, liveActivitiesAllowed: state.liveActivity.allowedInSettings,
                                     live: state.liveActivity, vehicleID: vehicleID)
        }
    }
}
