import SwiftUI

/// Trip planner defaults, read and changed on the phone: the drive mode and Tesla NACS adapter the planner pre-fills, and the gas
/// price and MPG behind the gas-equivalent chip. The web's card, with its labels. Only what changed is sent; the saved values shown are
/// the server's own answer.
struct PlannerDefaultsView: View {
    @Environment(AppState.self) private var state
    @State private var model = SettingsCardModel<PlannerDefaults>()
    /// The server answers 404 while its trip planner is off: there is nothing to set.
    @State private var plannerOff = false

    var body: some View {
        SettingsStack {
            ScrollView {
                ThemeSection(title: "Trip planner defaults") {
                    if plannerOff {
                        Text("The trip planner isn't turned on for this server.").themeText(.value, color: Theme.textSecondary)
                    } else {
                    CardLoadView(model: model, what: "the planner defaults", onReload: reload) { saved in
                        PlannerDefaultsEditor(saved: saved, model: model, serverURL: state.serverURL, onSave: { draft in
                            Task { await model.save { try await state.client.savePlannerDefaults(draft, saved: saved) } }
                        }, onReload: reload)
                            .id(saved)
                    }
                    }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Trip planner defaults")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
        .task { await load() }
    }

    private func reload() { Task { await load() } }

    private func load() async {
        plannerOff = false
        await model.load {
            do {
                return try await state.client.plannerDefaults()
            } catch APIError.server(404, _) {
                plannerOff = true
                throw APIError.server(status: 404, message: "")
            }
        }
    }
}

struct PlannerDefaultsEditor: View {
    let saved: PlannerDefaults
    let model: SettingsCardModel<PlannerDefaults>
    let serverURL: URL
    let onSave: (PlannerDefaultsForm.Draft) -> Void
    let onReload: () -> Void

    @State private var draft: PlannerDefaultsForm.Draft
    @FocusState private var focused: Bool

    init(saved: PlannerDefaults, model: SettingsCardModel<PlannerDefaults>, serverURL: URL,
         onSave: @escaping (PlannerDefaultsForm.Draft) -> Void, onReload: @escaping () -> Void) {
        self.saved = saved; self.model = model; self.serverURL = serverURL; self.onSave = onSave; self.onReload = onReload
        _draft = State(initialValue: PlannerDefaultsForm.draft(saved))
    }

    private var errors: [PlannerDefaultsForm.FieldError] { PlannerDefaultsForm.errors(draft) }
    private var canSave: Bool { errors.isEmpty && PlannerDefaultsForm.changed(draft, saved: saved) && model.saveState != .saving }

    /// A drive mode stored on the account that this card doesn't offer stays selectable so opening the card doesn't change it.
    private var modes: [PlannerDefaultsForm.DriveMode] {
        PlannerDefaultsForm.driveModes.contains { $0.value == draft.driveMode }
            ? PlannerDefaultsForm.driveModes : PlannerDefaultsForm.driveModes + [.init(value: draft.driveMode, title: draft.driveMode.capitalized)]
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            LabeledBox(title: PlannerDefaultsForm.driveModeLabel) {
                Picker(PlannerDefaultsForm.driveModeLabel, selection: $draft.driveMode) {
                    ForEach(modes) { Text($0.title).tag($0.value) }
                }
                .pickerStyle(.menu).tint(Theme.textPrimary).frame(minHeight: 44)
            }
            if saved.askAdapter {
                LabeledBox(title: PlannerDefaultsForm.adapterLabel) {
                    Picker(PlannerDefaultsForm.adapterLabel, selection: $draft.adapter) {
                        ForEach(PlannerDefaultsForm.Adapter.allCases) { Text($0.title).tag($0) }
                    }
                    .pickerStyle(.menu).tint(Theme.textPrimary).frame(minHeight: 44)
                }
            }
            HStack(alignment: .bottom, spacing: 12) {
                LabeledBox(title: PlannerDefaultsForm.gasLabel) {
                    TextField("4.00", text: $draft.gas).keyboardType(.decimalPad).focused($focused).settingsFieldBox()
                        .accessibilityLabel("Gas price per gallon")
                }
                LabeledBox(title: PlannerDefaultsForm.mpgLabel) {
                    TextField("20", text: $draft.mpg).keyboardType(.decimalPad).focused($focused).settingsFieldBox()
                        .accessibilityLabel("Comparison miles per gallon")
                }
            }
            ForEach(errors, id: \.message) { Text($0.message).themeText(.subline, color: Theme.estimate) }
            Text(PlannerDefaultsForm.gasNote).themeText(.caption, color: Theme.textTertiary)

            Button {
                focused = false
                onSave(draft)
            } label: {
                Text(model.saveState == .saving ? "Saving…" : "Save defaults").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeButtonStyle())
            .disabled(!canSave)

            SaveStatusView(model: model, savedText: { _ in "Saved." }, webURL: ChargingWebLink.url(serverURL, anchor: "planner"), onReload: onReload)
        }
        .onChange(of: draft) { _, _ in model.clearStatus() }
    }
}
