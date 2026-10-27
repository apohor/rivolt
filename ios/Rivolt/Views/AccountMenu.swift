import SwiftUI

/// The person icon at the end of every screen's top bar. A short menu: Refresh (when the screen has something to refresh by hand),
/// Settings and Sign out. Settings is one grouped screen (`SettingsView`) that pushes its detail screens, so the menu no longer lists
/// them all. The demo keeps its own short list (nothing to change, nothing to sign out of). Each screen only places it.
struct AccountMenu: View {
    @Environment(AppState.self) private var state
    /// The truck the vehicle and notification settings are for (the Live Activity switch is per truck).
    var vehicleID: String?
    /// Nil when the screen has nothing to refresh by hand.
    var onRefresh: (() -> Void)?

    @AppStorage(AppearanceChoice.storageKey) private var appearance = AppearanceChoice.system
    @State private var showSettings = false
    @State private var showAcknowledgements = false
    @State private var showAbout = false

    var body: some View {
        HStack(spacing: 2 * Theme.unit) {
            if state.isDemo { DemoPill() }
            menu
        }
    }

    private var menu: some View {
        Menu {
            if let onRefresh {
                Button("Refresh", action: onRefresh)
            }
            if state.isDemo {
                // The demo reads only: no notifications, no rates to change, no connection to a Rivian.
                Picker("Appearance", selection: $appearance) {
                    ForEach(AppearanceChoice.allCases) { Text($0.title).tag($0) }
                }
                Link("Privacy policy", destination: state.serverURL.appending(path: "privacy"))
                Link("Terms", destination: state.serverURL.appending(path: "terms"))
                Button("About the demo") { showAbout = true }
                Button("Acknowledgements") { showAcknowledgements = true }
                Divider()
                Button(state.demoResumesAccount ? "Back to my account" : "Sign in") { state.leaveDemo() }
            } else {
                Button("Settings") { showSettings = true }
                Divider()
                Button("Sign out", role: .destructive) {
                    Task { await state.logout() }
                }
            }
        } label: {
            Image(systemName: "person.crop.circle").font(.title3).foregroundStyle(Theme.textSecondary)
        }
        .accessibilityLabel("Account")
        .sheet(isPresented: $showAbout) { DemoAboutSheet().followsAppearance() }
        .sheet(isPresented: $showAcknowledgements) { AcknowledgementsView().followsAppearance() }
        .sheet(isPresented: $showSettings) { SettingsView(vehicleID: vehicleID).followsAppearance() }
    }
}
