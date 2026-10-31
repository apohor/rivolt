import SwiftUI

enum AccountMenuLogic {
    /// What VoiceOver says, and the least a tap target is (Apple's 44 pt).
    static let settingsLabel = "Settings"
    static let tapSize: CGFloat = 44
}

/// The person icon at the end of every screen's top bar: one tap opens Settings (which holds Sign out too). A screen with something to
/// refresh by hand also gets a small "more" menu with Refresh. The demo keeps its own short menu (nothing to change, nothing to sign out
/// of). Each screen only places it.
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
        HStack(spacing: 0) {
            if state.isDemo {
                DemoPill()
                menu
            } else {
                // Settings is one tap from every screen (owner decision). Refresh, where a screen has one, is its own small menu.
                if let onRefresh { refreshMenu(onRefresh) }
                settingsButton
            }
        }
        .sheet(isPresented: $showSettings) { SettingsView(vehicleID: vehicleID).followsAppearance() }
    }

    private var settingsButton: some View {
        Button { showSettings = true } label: {
            Image(systemName: "person.crop.circle").font(.title3).foregroundStyle(Theme.textSecondary)
                .frame(width: AccountMenuLogic.tapSize, height: AccountMenuLogic.tapSize)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(AccountMenuLogic.settingsLabel)
        .accessibilityIdentifier("settings-button")
    }

    private func refreshMenu(_ onRefresh: @escaping () -> Void) -> some View {
        Menu {
            Button("Refresh", action: onRefresh)
        } label: {
            Image(systemName: "ellipsis.circle").font(.title3).foregroundStyle(Theme.textSecondary)
                .frame(width: AccountMenuLogic.tapSize, height: AccountMenuLogic.tapSize)
                .contentShape(Rectangle())
        }
        .accessibilityLabel("More")
    }

    private var menu: some View {
        Menu {
            if let onRefresh {
                Button("Refresh", action: onRefresh)
            }
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
        } label: {
            Image(systemName: "person.crop.circle").font(.title3).foregroundStyle(Theme.textSecondary)
        }
        .accessibilityLabel("Account")
        .sheet(isPresented: $showAbout) { DemoAboutSheet().followsAppearance() }
        .sheet(isPresented: $showAcknowledgements) { AcknowledgementsView().followsAppearance() }
    }
}
