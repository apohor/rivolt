import SwiftUI
import UIKit

/// App entry point. Responsible for one thing only: constructing the
/// long-lived `AppState` and handing it to the root view. All session
/// / network / settings state lives inside `AppState` so the SwiftUI
/// preview surface can swap in a fixture instance without touching
/// URLSession or the Keychain.
@main
struct RivoltApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var state = AppState()

    var body: some Scene {
        WindowGroup {
            Group {
                #if DEBUG
                if let preview = PreviewHost.requested {
                    PreviewHost(screen: preview)
                } else {
                    root
                }
                #else
                root
                #endif
            }
        }
    }

    private var root: some View {
        RootView()
            .environment(state)
            // Kick a session-restore probe on cold launch. If the stored
            // token is still valid, the user lands directly on HomeView
            // instead of the login screen.
            .task { await state.bootstrap() }
    }
}

/// Apple hands the push device token to the app delegate; SwiftUI has no other
/// hook for it. It goes straight to the notification manager.
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        Task { @MainActor in NotificationManager.shared.didRegister(deviceToken: deviceToken) }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        Task { @MainActor in NotificationManager.shared.didFailToRegister(error) }
    }
}
