#if DEBUG
import SwiftUI

/// Fixed screens for CI screenshots (`-rivolt-preview <name>` as a launch
/// argument): no network, no Keychain and no iOS prompts, so the simulator
/// shows the same thing every run, in light and in dark.
struct PreviewHost: View {
    enum Screen: String {
        case prompt, notifications, notificationsDenied = "notifications-denied"
        case signin, signinUnavailable = "signin-unavailable"
        case notificationsTest = "notifications-test"
        case acknowledgements
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
        case .notificationsTest:
            NotificationSettingsView(manager: .preview(authorization: .authorized, registered: true, test: .sent(1)))
        case .signin:
            LoginView().environment(AppState.preview(signIn: .available))
        case .signinUnavailable:
            LoginView().environment(AppState.preview(signIn: .unavailable(.notOnThisServer)))
        case .acknowledgements:
            AcknowledgementsView()
        }
    }
}

extension AppState {
    /// A fixed sign-in state for simulator screenshots (no network is asked).
    static func preview(signIn: SignInStatus) -> AppState {
        let s = AppState()
        s.setPreviewSignIn(signIn)
        return s
    }
}
#endif
