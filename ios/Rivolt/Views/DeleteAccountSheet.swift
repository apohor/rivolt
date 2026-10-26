import SwiftUI

/// "Delete account": says what deleting does and opens the web page that does it (Settings, the Danger zone), where the
/// person confirms. The app never deletes anything itself: its token can't (a leaked phone token must not be able to).
/// Apple's guideline 5.1.1(v) allows a direct link to the page that completes the deletion.
struct DeleteAccountSheet: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL

    /// The web page that deletes the account: Settings, Data, the Danger zone.
    static func pageURL(server: URL) -> URL? {
        URL(string: "/settings?tab=data#danger", relativeTo: server)?.absoluteURL
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
            Text("Delete your account").font(Theme.sans(ThemeTokens.typeHeadline, .semibold, relativeTo: .title3)).foregroundStyle(Theme.textPrimary)
                .accessibilityAddTraits(.isHeader)
            Text(Self.explanation).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
            if let url = Self.pageURL(server: state.serverURL) {
                Button {
                    state.accountDeletionStarted()
                    openURL(url)
                    dismiss()
                } label: {
                    Text("Open account deletion").frame(maxWidth: .infinity)
                }
                .buttonStyle(ThemeButtonStyle())
            }
            Button("Not now") { dismiss() }
                .buttonStyle(ThemeSecondaryButtonStyle())
            Spacer(minLength: 0)
        }
        .padding(6 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .themePage()
        .presentationDetents([.medium])
    }

    static let explanation = "Deleting your account removes your drives, charges, settings and connected Rivian from Rivolt, and signs out every device. It can't be undone. It is done on the web, where you confirm it; this opens that page in your browser."
}
