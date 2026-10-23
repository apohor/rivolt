import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

/// Notifications on this iPhone: whether they're on, the five switches the web
/// Settings has, and how to turn them off. Reached from the Home menu, and only
/// when the server offers notifications.
struct NotificationSettingsView: View {
    let manager: NotificationManager
    /// Whether iOS lets Rivolt show Live Activities (iOS Settings); for the hint under that switch.
    var liveActivitiesAllowed = true
    /// For "Send test Live Activity" (nil in previews and where there is none).
    var live: LiveActivityManager? = nil
    var vehicleID: String? = nil
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    ThemeSection(title: "This iPhone") {
                        statusRow
                        Text("Apple's push service sees only this phone's push address and a generic “Rivolt update”. The words are fetched from Rivolt over an encrypted connection when it arrives.")
                            .themeText(.caption)
                            .fixedSize(horizontal: false, vertical: true)
                    }

                    // Always here, so it is never missing: it works once notifications are on for this iPhone, and says
                    // what is missing until then.
                    ThemeSection(title: "Test") {
                        Button {
                            Task { await manager.sendTest() }
                        } label: {
                            HStack(spacing: 2 * Theme.unit) {
                                Text("Send test notification")
                                if manager.test == .sending { ProgressView().tint(Theme.textSecondary) }
                            }
                        }
                        .buttonStyle(ThemeSecondaryButtonStyle())
                        .disabled(!manager.isOn || manager.test == .sending)
                        Group {
                            if !manager.isOn {
                                Text("Turn on notifications for this iPhone first.").themeText(.caption)
                            } else {
                                switch manager.test {
                                case .sent(let n):
                                    Text("Sent to \(n) iPhone\(n == 1 ? "" : "s"). It should arrive in a moment.").themeText(.caption)
                                case .failed(let message):
                                    Text(message).themeText(.caption, color: Theme.critical)
                                default:
                                    Text("Sends one test to your iPhones signed in to Rivolt.").themeText(.caption)
                                }
                            }
                        }
                        .fixedSize(horizontal: false, vertical: true)
                    }

                    if let live, manager.liveOffered, manager.isOn, manager.preferences?.onChargingLive == true {
                        ThemeSection(title: "Live Activity") {
                            Button {
                                Task { await live.sendTest(vehicleID: vehicleID, deviceToken: manager.deviceToken) }
                            } label: {
                                HStack(spacing: 2 * Theme.unit) {
                                    Text("Send test Live Activity")
                                    if live.test == .working { ProgressView().tint(Theme.textSecondary) }
                                }
                            }
                            .buttonStyle(ThemeSecondaryButtonStyle())
                            .disabled(live.test == .working || !liveActivitiesAllowed)
                            Group {
                                switch live.test {
                                case .sent:
                                    Text("Test sent. The Lock Screen card should show 50%, 7.2 kW and 60 min (test numbers), then end in about 20 seconds.").themeText(.caption)
                                case .failed(let message):
                                    Text(message).themeText(.caption, color: Theme.critical)
                                default:
                                    Text("Starts a Live Activity with test numbers and asks the server to update and end it. Lock the phone to see it.").themeText(.caption)
                                }
                            }
                            .fixedSize(horizontal: false, vertical: true)
                        }
                    }

                    if let prefs = manager.preferences {
                        ThemeSection(title: "Notify me when…") {
                            let kinds = PushKind.allCases.filter {
                                ($0 != .chargingLive || (manager.liveOffered && prefs.chargingLiveKnown)) && ($0 != .leftOpen || prefs.leftOpenKnown)
                            }
                            ForEach(Array(kinds.enumerated()), id: \.element) { index, kind in
                                if index > 0 { Rectangle().fill(Theme.border).frame(height: 1) }
                                Toggle(isOn: binding(for: kind, in: prefs)) {
                                    VStack(alignment: .leading, spacing: Theme.unit / 2) {
                                        Text(kind.title).themeText(.value, color: Theme.textPrimary)
                                        if let help = kind.help {
                                            Text(help).themeText(.caption)
                                        }
                                    }
                                }
                                .tint(Theme.accentFill)
                            }
                            if manager.liveOffered && prefs.chargingLiveKnown {
                                Text("Starts when you open Rivolt while the truck is charging, and ends when the charge does.").themeText(.caption)
                                if !liveActivitiesAllowed {
                                    Text("Live Activities are turned off for Rivolt in iOS Settings.").themeText(.caption, color: Theme.estimate)
                                }
                            }
                            if let error = manager.switchError {
                                Text(error).themeText(.caption, color: Theme.critical)
                            }
                        }
                    }

                    if manager.isOn {
                        Button(role: .destructive) {
                            Task { await manager.turnOff() }
                        } label: {
                            Text("Turn off notifications on this iPhone")
                                .font(Theme.sans(ThemeTokens.typeValue, .medium))
                                .foregroundStyle(Theme.critical)
                                .frame(maxWidth: .infinity)
                        }
                        .themeCard()
                    }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Notifications")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                        .font(Theme.sans(ThemeTokens.typeValue, .medium))
                        .tint(Theme.accent)
                }
            }
        }
    }

    @ViewBuilder
    private var statusRow: some View {
        switch (manager.authorization, manager.registration) {
        case (.denied, _):
            VStack(alignment: .leading, spacing: 2 * Theme.unit) {
                status("Off in iOS Settings", icon: "bell.slash", tint: Theme.textSecondary)
                Text("Turn notifications on for Rivolt in the iOS Settings app to get them.").themeText(.subline)
                Button("Open iOS Settings") { openSystemSettings() }
                    .buttonStyle(ThemeSecondaryButtonStyle())
            }
        case (.notDetermined, _):
            VStack(alignment: .leading, spacing: 2 * Theme.unit) {
                status("Not turned on", icon: "bell", tint: Theme.textSecondary)
                Button { Task { await manager.turnOn() } } label: {
                    Text("Turn on notifications").frame(maxWidth: .infinity)
                }
                .buttonStyle(ThemeButtonStyle())
            }
        case (.authorized, .registered):
            // The icon carries the accent; the words stay primary, as on the web's Live pill text.
            status("On for this iPhone", icon: "bell.badge", tint: Theme.accent)
        case (.authorized, .failed(let message)):
            VStack(alignment: .leading, spacing: 2 * Theme.unit) {
                status(message, icon: "exclamationmark.triangle", tint: Theme.estimate)
                Button("Try again") { Task { await manager.turnOn() } }
                    .buttonStyle(ThemeSecondaryButtonStyle())
            }
        case (.authorized, _):
            HStack(spacing: 2 * Theme.unit) {
                ProgressView().tint(Theme.textSecondary)
                Text("Setting up…").themeText(.value, color: Theme.textPrimary)
            }
        }
    }

    private func status(_ text: String, icon: String, tint: Color) -> some View {
        Label {
            Text(text).themeText(.value, color: Theme.textPrimary)
        } icon: {
            Image(systemName: icon).foregroundStyle(tint)
        }
    }

    private func binding(for kind: PushKind, in prefs: PushPreferences) -> Binding<Bool> {
        Binding(
            get: { manager.preferences?[kind] ?? prefs[kind] },
            set: { on in Task { await manager.set(kind, to: on) } }
        )
    }

    private func openSystemSettings() {
        #if canImport(UIKit)
        if let url = URL(string: UIApplication.openSettingsURLString) {
            UIApplication.shared.open(url)
        }
        #endif
    }
}

/// Rivolt's own ask, shown on Home once the server offers notifications and
/// before iOS has been asked. "Turn on" raises the iOS prompt; "Not now" is
/// remembered and the switch stays in Settings.
struct NotificationPromptCard: View {
    let manager: NotificationManager

    var body: some View {
        VStack(alignment: .leading, spacing: 3 * Theme.unit) {
            Label {
                Text("Get a notification when charging finishes")
                    .font(Theme.sans(ThemeTokens.typeValue, .semibold))
                    .foregroundStyle(Theme.textPrimary)
            } icon: {
                Image(systemName: "bell.badge").foregroundStyle(Theme.accent)
            }
            Text("Rivolt can tell you when a charge completes. Apple only gets a generic “Rivolt update”; the words are fetched from Rivolt when the notification arrives.")
                .themeText(.subline)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 3 * Theme.unit) {
                Button { Task { await manager.turnOn() } } label: {
                    Text("Turn on").frame(maxWidth: .infinity)
                }
                .buttonStyle(ThemeButtonStyle())
                Button("Not now") { manager.dismissPrompt() }
                    .buttonStyle(ThemeSecondaryButtonStyle())
            }
        }
        .themeCard()
    }
}
