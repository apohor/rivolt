import SwiftUI

/// The Trip planner page, read and changed on the phone: which chargers the map shows (how far off the route, hidden near the start, the smallest
/// fast charger, the pins on to begin with, preferred networks), the drive mode and Tesla NACS adapter the planner pre-fills, the map's weather and radar
/// defaults, and the gas price and MPG behind the gas-equivalent chip. Only what changed is sent (the whole set of planner settings, when it is); the saved
/// values shown are the server's own answer.
struct PlannerDefaultsView: View {
    @Environment(AppState.self) private var state
    @State private var model = SettingsCardModel<PlannerDefaults>()
    /// The server answers 404 while its trip planner is off: there is nothing to set.
    @State private var plannerOff = false

    var body: some View {
        SettingsStack {
            ScrollView {
                ThemeSection(title: "Trip planner") {
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
            .navigationTitle("Trip planner")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
        .task { await load() }
        // The Radar default row is shown only where the server has radar.
        .task { await state.radar.probe(source: state.client) }
    }

    private func reload() { Task { await load() } }

    private func load() async {
        plannerOff = false
        await model.load {
            do {
                return try await state.client.plannerDefaults()
            } catch APIError.server(404, _, _) {
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

    @Environment(AppState.self) private var appState
    @Environment(\.dynamicTypeSize) private var typeSize
    private var metric: Bool { NowFormat.usesMetric }

    private func group<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(title).themeText(.label, color: Theme.textSecondary).accessibilityAddTraits(.isHeader)
            content()
        }
    }

    private var gasField: some View {
        LabeledBox(title: PlannerDefaultsForm.gasLabel) {
            TextField("4.00", text: $draft.gas).keyboardType(.decimalPad).focused($focused).settingsFieldBox().accessibilityLabel("Gas price per gallon")
        }
    }

    private var mpgField: some View {
        LabeledBox(title: PlannerDefaultsForm.mpgLabel) {
            TextField("20", text: $draft.mpg).keyboardType(.decimalPad).focused($focused).settingsFieldBox().accessibilityLabel("Comparison miles per gallon")
        }
    }

    /// A binding into the draft's map settings (which are there wherever these rows are shown).
    private func mapBinding<V>(_ key: WritableKeyPath<PlannerMapSettings, V>) -> Binding<V> {
        Binding(get: { (draft.map ?? PlannerMapSettings())[keyPath: key] }, set: { draft.map?[keyPath: key] = $0 })
    }

    private func layerBinding(_ kind: String) -> Binding<Bool> {
        Binding(get: { draft.map?.layersOn.contains(kind) ?? false },
                set: { on in if on { draft.map?.layersOn.insert(kind) } else { draft.map?.layersOn.remove(kind) } })
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            if draft.map != nil {
                group(PlannerSettingsCopy.chargersGroup) {
                    PlannerChoiceRow(label: PlannerSettingsCopy.corridorLabel, note: PlannerSettingsCopy.corridorNote(metric: metric),
                                     choices: PlannerSettingsCopy.corridors, selection: mapBinding(\.corridor))
                    PlannerChoiceRow(label: PlannerSettingsCopy.startLabel, note: PlannerSettingsCopy.startNote,
                                     choices: PlannerSettingsCopy.startChoices(metric: metric), selection: mapBinding(\.startExcludeMi))
                    PlannerChoiceRow(label: PlannerSettingsCopy.minFastLabel, note: PlannerSettingsCopy.minFastNote,
                                     choices: PlannerSettingsCopy.minFast, selection: mapBinding(\.minFastKw))
                    VStack(alignment: .leading, spacing: 4) {
                        Text(PlannerSettingsCopy.layersLabel).themeText(.label)
                        ForEach(PlannerSettingsCopy.layers, id: \.kind) { layer in
                            Toggle(layer.title, isOn: layerBinding(layer.kind)).tint(Theme.accentButton).frame(minHeight: 44)
                        }
                        Text(PlannerSettingsCopy.layersNote).themeText(.caption, color: Theme.textTertiary)
                    }
                    PlannerToggleRow(label: PlannerSettingsCopy.preferredLabel, note: PlannerSettingsCopy.preferredNote, isOn: mapBinding(\.preferredOnly))
                }
            }
            group(PlannerSettingsCopy.planningGroup) {
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
            }
            if draft.map != nil {
                group(PlannerSettingsCopy.mapGroup) {
                    PlannerToggleRow(label: PlannerSettingsCopy.weatherLabel, note: PlannerSettingsCopy.weatherNote, isOn: mapBinding(\.weatherOn))
                    // Shown only where the server has radar (then the Radar button is on the map); elsewhere radar_on is sent back as it was read.
                    if PlannerSettingsCopy.showsRadarRow(appState.radar.availability) {
                        PlannerToggleRow(label: PlannerSettingsCopy.radarLabel, note: PlannerSettingsCopy.radarNote, isOn: mapBinding(\.radarOn))
                    }
                }
            }
            group(PlannerSettingsCopy.costsGroup) {
                if typeSize.isAccessibilitySize {
                    gasField; mpgField
                } else {
                    HStack(alignment: .bottom, spacing: 12) { gasField; mpgField }
                }
                ForEach(errors, id: \.message) { Text($0.message).themeText(.subline, color: Theme.estimate) }
                Text(PlannerDefaultsForm.gasNote).themeText(.caption, color: Theme.textTertiary)
            }

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

/// A setting with a few choices and the one line under it. A segmented control while the text is normal; at accessibility sizes (where the segments
/// would not fit) a menu, full width, so every choice stays readable and reachable.
struct PlannerChoiceRow<Value: Hashable>: View {
    let label: String
    let note: String
    let choices: [PlannerSettingsCopy.Choice<Value>]
    @Binding var selection: Value
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(label).themeText(.label)
            if typeSize.isAccessibilitySize {
                Picker(label, selection: $selection) { ForEach(choices) { Text($0.title).tag($0.value) } }
                    .pickerStyle(.menu).tint(Theme.textPrimary).frame(minHeight: 44, alignment: .leading)
            } else {
                Picker(label, selection: $selection) { ForEach(choices) { Text($0.title).tag($0.value) } }
                    .pickerStyle(.segmented).frame(minHeight: 44)
            }
            Text(note).themeText(.caption, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
        }
    }
}

struct PlannerToggleRow: View {
    let label: String
    let note: String
    @Binding var isOn: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Toggle(label, isOn: $isOn).tint(Theme.accentButton).frame(minHeight: 44)
            Text(note).themeText(.caption, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
        }
    }
}
