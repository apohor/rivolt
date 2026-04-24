import SwiftUI

/// App entry point. Responsible for one thing only: constructing the
/// long-lived `AppState` and handing it to the root view. All session
/// / network / settings state lives inside `AppState` so the SwiftUI
/// preview surface can swap in a fixture instance without touching
/// URLSession or the Keychain.
@main
struct RivoltApp: App {
    @State private var state = AppState()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(state)
                // Kick a session-restore probe on cold launch. If the
                // session cookie from a previous run is still valid,
                // the user lands directly on HomeView instead of the
                // login screen.
                .task { await state.bootstrap() }
        }
    }
}
