#if DEBUG
import SwiftUI

/// Fixed screens for CI screenshots (`-rivolt-preview <name>` as a launch
/// argument): no network, no Keychain and no iOS prompts, so the simulator
/// shows the same thing every run, in light and in dark.
struct PreviewHost: View {
    enum Screen: String {
        case prompt, notifications, notificationsDenied = "notifications-denied"
    }

    let screen: Screen

    /// The screen asked for on the launch line, if any.
    static var requested: Screen? {
        UserDefaults.standard.string(forKey: "rivolt-preview").flatMap(Screen.init(rawValue:))
    }

    var body: some View {
        switch screen {
        case .prompt:
            NavigationStack {
                VStack {
                    NotificationPromptCard(manager: .preview(authorization: .notDetermined, registered: false))
                        .padding()
                    Spacer()
                }
                .navigationTitle("Rivolt")
            }
        case .notifications:
            NotificationSettingsView(manager: .preview(authorization: .authorized, registered: true))
        case .notificationsDenied:
            NotificationSettingsView(manager: .preview(authorization: .denied, registered: false))
        }
    }
}
#endif
