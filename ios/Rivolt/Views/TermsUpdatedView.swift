import SwiftUI

/// What a signed-in account sees while the server wants the updated Terms accepted: one screen in place of cards that would
/// all fail. The Terms are accepted on the web (the app doesn't accept anything for the person); back here, it looks again.
enum TermsUpdated {
    static let title = "Accept the updated Terms to continue"
    static let explanation = "Rivolt's Terms have been updated. Open Rivolt on the web, where the updated Terms appear, and accept them there. Then come back here."
    static func updatedOn(_ version: String?) -> String? { version.flatMap { $0.isEmpty ? nil : "Terms updated \($0)" } }
    static let dataLine = "Export or delete your data"

    /// Export and deleting the account stay open to an account that has not accepted: Settings, Data, on the web.
    static func dataURL(_ server: URL) -> URL {
        var c = URLComponents(url: server.appending(path: "settings"), resolvingAgainstBaseURL: false)
        c?.queryItems = [URLQueryItem(name: "tab", value: "data")]
        return c?.url ?? server
    }
}

struct TermsUpdatedView: View {
    @Environment(AppState.self) private var state
    @State private var checking = false

    var body: some View {
        ScrollView {
            TermsUpdatedContent(open: state.serverURL, version: state.termsVersion, dataURL: TermsUpdated.dataURL(state.serverURL), checking: checking, onCheck: {
                Task { checking = true; await state.recheckTerms(); checking = false }
            })
        }
        .themePage()
        .safeAreaInset(edge: .top, spacing: 0) { ThemeTopBar { AccountMenu() } }
    }
}

struct TermsUpdatedContent: View {
    var open: URL
    var version: String? = nil
    var dataURL: URL? = nil
    var checking = false
    var onCheck: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
            Text(TermsUpdated.title).font(Theme.sans(20, .semibold)).foregroundStyle(Theme.textPrimary)
                .accessibilityAddTraits(.isHeader)
            if let line = TermsUpdated.updatedOn(version) { Text(line).themeText(.subline, color: Theme.textSecondary) }
            Text(TermsUpdated.explanation).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
            Link(destination: open) {
                Text("Open Rivolt on the web").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeButtonStyle())
            Button(action: onCheck) {
                Text(checking ? "Checking…" : "I've accepted, check again").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeSecondaryButtonStyle())
            .disabled(checking)
            if let dataURL {
                Link(TermsUpdated.dataLine, destination: dataURL).font(Theme.sans(ThemeTokens.typeSubline, .medium)).tint(Theme.accent)
            }
        }
        .padding(6 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .themeCard(padding: 0)
        .padding(4 * Theme.unit)
    }
}
