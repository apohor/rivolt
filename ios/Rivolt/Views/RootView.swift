import SwiftUI

/// Top-level router. Single switch on `AppState.phase` so there's
/// exactly one place that decides which screen is on-screen — adding
/// a new phase (e.g. `.locked` for biometric re-auth) becomes a
/// one-line change here plus a new case.
struct RootView: View {
    @Environment(AppState.self) private var state

    var body: some View {
        switch state.phase {
        case .bootstrapping:
            SplashView()
        case .loggedOut:
            LoginView()
        case .loggedIn:
            HomeView()
        }
    }
}

private struct SplashView: View {
    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "bolt.car.fill")
                .font(.system(size: 56, weight: .semibold))
                .foregroundStyle(.tint)
            ProgressView()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(.systemBackground))
    }
}
