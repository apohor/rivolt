import SwiftUI

/// Whether a screen is pushed inside Settings (its own navigation bar and no Done button) or shown on its own as a sheet.
private struct InSettingsKey: EnvironmentKey { static let defaultValue = false }

extension EnvironmentValues {
    var inSettings: Bool {
        get { self[InSettingsKey.self] }
        set { self[InSettingsKey.self] = newValue }
    }
}

/// Stands where a settings detail screen put its own `NavigationStack`: a stack of its own as a sheet, none when Settings pushed it
/// (a second stack inside a pushed screen would draw a second bar).
struct SettingsStack<Content: View>: View {
    @Environment(\.inSettings) private var inSettings
    @ViewBuilder var content: Content

    var body: some View {
        if inSettings { content } else { NavigationStack { content } }
    }
}

/// The Done button of a settings screen shown as a sheet; nothing when it is pushed (the back button is there).
struct SheetDoneButton: View {
    @Environment(\.inSettings) private var inSettings
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        if !inSettings {
            Button("Done") { dismiss() }
                .font(Theme.sans(ThemeTokens.typeValue, .medium))
                .tint(Theme.accent)
        }
    }
}
