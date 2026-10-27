import SwiftUI

/// The account menu's Appearance switch: follow the phone, or stay light or dark. Saved on this iPhone only.
/// The widgets and the Live Activity follow the system appearance (iOS gives them no app-side override).
enum AppearanceChoice: String, CaseIterable, Identifiable {
    case system, light, dark

    static let storageKey = "rivolt.appearance"

    var id: String { rawValue }

    var title: String {
        switch self {
        case .system: return "System"
        case .light: return "Light"
        case .dark: return "Dark"
        }
    }

    /// Nil leaves the choice to the system.
    var scheme: ColorScheme? {
        switch self {
        case .system: return nil
        case .light: return .light
        case .dark: return .dark
        }
    }
}

/// The chosen appearance, applied live. A sheet is its own presentation and does not always take a change made under it from the app
/// root's `preferredColorScheme`, so the root and every sheet (and what a sheet pushes) read the same setting through this one modifier.
private struct FollowsAppearance: ViewModifier {
    @AppStorage(AppearanceChoice.storageKey) private var appearance = AppearanceChoice.system

    func body(content: Content) -> some View {
        content.preferredColorScheme(appearance.scheme)
    }
}

extension View {
    func followsAppearance() -> some View { modifier(FollowsAppearance()) }
}
