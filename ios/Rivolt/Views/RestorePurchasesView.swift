import SwiftUI

/// Restore purchases where billing is off (a setup or a rollback): an account that already pays through Apple can still ask for its
/// subscription to be found again. It sells nothing, so it does not wait for billing to be on.
struct RestorePurchasesView: View {
    @Environment(AppState.self) private var state
    @State private var busy = false
    @State private var message: String?

    var body: some View {
        SettingsStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    Text(ProCopy.restoreHelp).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                    Button { Task { await restore() } } label: { Text(ProCopy.restore).frame(maxWidth: .infinity) }
                        .buttonStyle(ThemeSecondaryButtonStyle()).disabled(busy)
                    if let message { Text(message).themeText(.subline, color: Theme.textSecondary).accessibilityAddTraits(.updatesFrequently) }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle(ProCopy.restore)
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
    }

    private func restore() async {
        busy = true
        message = nil
        defer { busy = false }
        do {
            try await state.makePurchasing(selling: false).restore()
            message = ProCopy.restored
        } catch { message = ProCopy.purchaseMessage(error) }
    }
}
