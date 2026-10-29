import SwiftUI
import UIKit
import UserNotifications

/// App entry point. Responsible for one thing only: constructing the
/// long-lived `AppState` and handing it to the root view. All session
/// / network / settings state lives inside `AppState` so the SwiftUI
/// preview surface can swap in a fixture instance without touching
/// URLSession or the Keychain.
@main
struct RivoltApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var state = AppState()

    init() {
        // The bundled Geist fonts, before the first screen asks for them.
        Theme.registerFonts()
    }

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
            .followsAppearance()
        }
    }

    private var root: some View {
        RootView()
            .environment(state)
            // Kick a session-restore probe on cold launch. If the stored
            // token is still valid, the user lands directly on HomeView
            // instead of the login screen.
            .task {
                await state.bootstrap()
                // Screenshots for the App Store (debug builds, launch argument only): open the demo truck.
                if StoreShots.name != nil { state.enterDemo() }
            }
            // Sign-in callbacks reach the app only through the in-flight
            // browser session (SignInFlow). A rivolt.dev/app/callback link
            // opened any other way holds no verifier to use: dropped, and
            // never logged (it carries a one-time code).
            .onOpenURL { _ in }
    }
}

/// Apple hands the push device token to the app delegate; SwiftUI has no other
/// hook for it. It goes straight to the notification manager.
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    /// The notification-centre delegate is set at launch. Without it iOS drops a notification that arrives
    /// while the app is open: the owner's test notification was accepted by Apple and never shown.
    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        return true
    }

    /// A notification that arrives while Rivolt is in the foreground is still shown.
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        NotificationPresentation.foreground
    }

    /// A tap opens the app on the screen the notification is about (its stored target; Home when unknown or on any failure).
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let route: NotificationRoute
        if let id = NotificationFetch.id(from: response.notification.request.content.userInfo) {
            route = await NotificationFetch.route(id: id)
        } else {
            route = .home
        }
        await MainActor.run { NotificationManager.shared.pendingRoute = route }
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        Task { @MainActor in NotificationManager.shared.didRegister(deviceToken: deviceToken) }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        Task { @MainActor in NotificationManager.shared.didFailToRegister(error) }
    }
}
