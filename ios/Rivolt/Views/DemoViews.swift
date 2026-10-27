import SwiftUI

private struct IsDemoKey: EnvironmentKey {
    static let defaultValue = false
}

extension EnvironmentValues {
    /// True while the demo truck is open: screens hide whatever would write or connect.
    var isDemo: Bool {
        get { self[IsDemoKey.self] }
        set { self[IsDemoKey.self] = newValue }
    }
}

/// The "Demo" pill in every top bar while the demo truck is open: it says what this is and is the way out ("Sign in").
struct DemoPill: View {
    @State private var showAbout = false

    var body: some View {
        Button { showAbout = true } label: {
            ThemePill(text: "DEMO", tone: .stale)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Demo. \(DemoMode.caption)")
        .accessibilityHint("Opens what the demo is, and how to sign in")
        .sheet(isPresented: $showAbout) { DemoAboutSheet().followsAppearance() }
    }
}

/// What the demo is, with the way to a real account.
struct DemoAboutSheet: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
            HStack(spacing: 2 * Theme.unit) {
                Text("The demo truck").font(Theme.sans(ThemeTokens.typeHeadline, .semibold, relativeTo: .title3)).foregroundStyle(Theme.textPrimary)
                ThemePill(text: "DEMO", tone: .stale)
            }
            .accessibilityElement(children: .combine)
            Text(DemoMode.caption).themeText(.subline, color: Theme.textTertiary)
            Text(DemoMode.about)
                .themeText(.value, color: Theme.textBody)
                .fixedSize(horizontal: false, vertical: true)
            Button {
                dismiss()
                state.leaveDemo()
            } label: {
                Text(state.demoResumesAccount ? "Back to my account" : "Sign in").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeButtonStyle())
            Button("Keep looking around") { dismiss() }
                .buttonStyle(ThemeSecondaryButtonStyle())
            Spacer(minLength: 0)
        }
        .padding(6 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .themePage()
        .presentationDetents([.medium])
    }
}

/// The sign-in screen's way into the demo: the button and what it is.
struct DemoEntryButton: View {
    var disabled = false
    let action: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.unit) {
            Button("Try the demo truck", action: action)
                .buttonStyle(ThemeSecondaryButtonStyle())
                .disabled(disabled)
            Text("A sample truck with made-up data. No account needed.").themeText(.caption)
        }
    }
}
