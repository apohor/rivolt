import SwiftUI

/// The off-peak prompt card on Money. It reads the off-peak answer once (and again when the sheet closes), and "Set off-peak hours" opens
/// the same Off-peak schedule screen as Settings → Charging.
struct OffpeakPromptView: View {
    @Environment(AppState.self) private var state
    @AppStorage(OffpeakPromptLogic.dismissedKey) private var dismissed = false
    @State private var schedule: OffpeakSchedule?
    @State private var editing = false

    var body: some View {
        Group {
            if OffpeakPromptLogic.shows(schedule, dismissed: dismissed, demo: state.isDemo) {
                VStack(alignment: .leading, spacing: 8) {
                    Text(OffpeakPromptLogic.title).themeText(.value, color: Theme.textPrimary)
                        .accessibilityAddTraits(.isHeader)
                    Text(OffpeakPromptLogic.body).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                    HStack(spacing: 12) {
                        Button(OffpeakPromptLogic.setButton) { editing = true }.buttonStyle(ThemeButtonStyle())
                        Button(OffpeakPromptLogic.laterButton) { dismissed = true }.buttonStyle(ThemeSecondaryButtonStyle())
                    }
                }
                .padding(4 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
                .themeCard(padding: 0)
                .accessibilityElement(children: .contain)
            }
        }
        .task { await load() }
        .sheet(isPresented: $editing, onDismiss: { Task { await load() } }) {
            OffpeakView().followsAppearance()
        }
    }

    private func load() async {
        guard !state.isDemo, !dismissed else { return }
        schedule = try? await state.client.offpeak()
    }
}
