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
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                Section {
                    statusRow
                } footer: {
                    Text("Apple's push service sees only this phone's push address and a generic “Rivolt update”. The words are fetched from Rivolt over an encrypted connection when it arrives.")
                }

                // Always here, so it is never missing: it works once notifications are on for this iPhone, and says
                // what is missing until then.
                Section {
                    Button {
                        Task { await manager.sendTest() }
                    } label: {
                        HStack {
                            Text("Send test notification")
                            if manager.test == .sending { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(!manager.isOn || manager.test == .sending)
                } footer: {
                    if !manager.isOn {
                        Text("Turn on notifications for this iPhone first.")
                    } else {
                        switch manager.test {
                        case .sent(let n):
                            Text("Sent to \(n) iPhone\(n == 1 ? "" : "s"). It should arrive in a moment.")
                        case .failed(let message):
                            Text(message).foregroundStyle(.red)
                        default:
                            Text("Sends one test to your iPhones signed in to Rivolt.")
                        }
                    }
                }

                if let prefs = manager.preferences {
                    Section {
                        ForEach(PushKind.allCases.filter { $0 != .chargingLive || (manager.liveOffered && prefs.chargingLiveKnown) }) { kind in
                            Toggle(isOn: binding(for: kind, in: prefs)) {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(kind.title)
                                    if let help = kind.help {
                                        Text(help)
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                    }
                                }
                            }
                        }
                    } header: {
                        Text("Notify me when…")
                    } footer: {
                        VStack(alignment: .leading, spacing: 4) {
                            if manager.liveOffered && prefs.chargingLiveKnown {
                                Text("Starts when you open Rivolt while the truck is charging, and ends when the charge does.")
                                if !liveActivitiesAllowed {
                                    Text("Live Activities are turned off for Rivolt in iOS Settings.").foregroundStyle(.orange)
                                }
                            }
                            if let error = manager.switchError {
                                Text(error).foregroundStyle(.red)
                            }
                        }
                    }
                }

                if manager.isOn {
                    Section {
                        Button("Turn off notifications on this iPhone", role: .destructive) {
                            Task { await manager.turnOff() }
                        }
                    }
                }
            }
            .navigationTitle("Notifications")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    @ViewBuilder
    private var statusRow: some View {
        switch (manager.authorization, manager.registration) {
        case (.denied, _):
            VStack(alignment: .leading, spacing: 8) {
                Label("Off in iOS Settings", systemImage: "bell.slash")
                Text("Turn notifications on for Rivolt in the iOS Settings app to get them.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                Button("Open iOS Settings") { openSystemSettings() }
            }
        case (.notDetermined, _):
            VStack(alignment: .leading, spacing: 8) {
                Label("Not turned on", systemImage: "bell")
                Button("Turn on notifications") { Task { await manager.turnOn() } }
            }
        case (.authorized, .registered):
            // The icon is green; the words stay the primary colour (green text on white is too faint).
            Label {
                Text("On for this iPhone")
            } icon: {
                Image(systemName: "bell.badge").foregroundStyle(.green)
            }
        case (.authorized, .failed(let message)):
            VStack(alignment: .leading, spacing: 8) {
                Label(message, systemImage: "exclamationmark.triangle")
                Button("Try again") { Task { await manager.turnOn() } }
            }
        case (.authorized, _):
            HStack(spacing: 8) {
                ProgressView()
                Text("Setting up…")
            }
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
        VStack(alignment: .leading, spacing: 10) {
            Label("Get a notification when charging finishes", systemImage: "bell.badge")
                .font(.subheadline.weight(.semibold))
            Text("Rivolt can tell you when a charge completes. Apple only gets a generic “Rivolt update”; the words are fetched from Rivolt when the notification arrives.")
                .font(.footnote)
                .foregroundStyle(.secondary)
            HStack(spacing: 12) {
                Button("Turn on") { Task { await manager.turnOn() } }
                    .buttonStyle(.borderedProminent)
                Button("Not now") { manager.dismissPrompt() }
                    .buttonStyle(.bordered)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 12))
    }
}
