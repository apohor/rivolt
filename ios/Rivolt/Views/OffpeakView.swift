import SwiftUI

/// The off-peak electricity schedule, read and changed on the phone: windows of the week with their own rate, in one time zone, as
/// the web's "Off-peak schedule" card. Hours outside every window are priced at the home rate. A save replaces the schedule and the
/// server's answer is what is shown as saved; the flat-rate choice is its own write.
struct OffpeakView: View {
    @Environment(AppState.self) private var state
    @State private var model = SettingsCardModel<OffpeakSchedule>()

    var body: some View {
        SettingsStack {
            ScrollView {
                ThemeSection(title: "Off-peak schedule") {
                    CardLoadView(model: model, what: "the schedule", onReload: reload) { saved in
                        OffpeakEditor(saved: saved, model: model, serverURL: state.serverURL, onSave: { put in
                            Task { await model.save { try await state.client.setOffpeak(put) } }
                        }, onFlat: { on in
                            Task { await model.save { var s = saved; s.flatRate = try await state.client.setFlatRate(on).flatRate; return s } }
                        }, onReload: reload)
                            .id("\(saved.tz)|\(saved.windows)")
                    }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Off-peak schedule")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
        .task { await model.load { try await state.client.offpeak() } }
    }

    private func reload() { Task { await model.load { try await state.client.offpeak() } } }
}

struct OffpeakEditor: View {
    let saved: OffpeakSchedule
    let model: SettingsCardModel<OffpeakSchedule>
    let serverURL: URL
    let onSave: (OffpeakPut) -> Void
    let onFlat: (Bool) -> Void
    let onReload: () -> Void

    @State private var rows: [OffpeakForm.Draft]
    @State private var tz: String
    @FocusState private var focused: Bool

    init(saved: OffpeakSchedule, model: SettingsCardModel<OffpeakSchedule>, serverURL: URL,
         onSave: @escaping (OffpeakPut) -> Void, onFlat: @escaping (Bool) -> Void, onReload: @escaping () -> Void) {
        self.saved = saved; self.model = model; self.serverURL = serverURL
        self.onSave = onSave; self.onFlat = onFlat; self.onReload = onReload
        _rows = State(initialValue: OffpeakForm.drafts(saved))
        _tz = State(initialValue: saved.tz.isEmpty ? OffpeakForm.deviceZone() : saved.tz)
    }

    private var currency: String { saved.currency ?? "USD" }
    private var homeRate: Double { saved.baseRate ?? 0 }
    private var errs: [OffpeakForm.WindowErrors] { OffpeakForm.validate(rows) }
    private var bad: Bool { OffpeakForm.hasErrors(errs) }
    private var dirty: Bool { OffpeakForm.changed(rows, tz: tz, saved: saved) }
    private var flat: Bool { saved.flatRate == true }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Toggle(isOn: Binding(get: { flat }, set: { onFlat($0) })) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(OffpeakForm.flatTitle).themeText(.value, color: Theme.textPrimary)
                    Text(OffpeakForm.flatNote(flat)).themeText(.caption, color: Theme.textTertiary)
                }
            }
            .tint(Theme.accent)
            .disabled(model.saveState == .saving)

            Text(OffpeakForm.intro + " Hours outside every window use your home rate"
                 + (homeRate > 0 ? " (\(NumberText.money(homeRate, currency, digits: 3))/kWh)." : "."))
                .themeText(.caption, color: Theme.textTertiary)
            if homeRate <= 0 && !rows.isEmpty {
                Text("Set your home rate in Charging rates too, so the hours outside these windows are priced.")
                    .themeText(.caption, color: Theme.estimate)
            }

            NavigationLink { TimeZonePickerView(selection: $tz) } label: {
                HStack {
                    Text("Times are in").themeText(.label)
                    Spacer()
                    Text(tz).themeText(.value, color: Theme.textPrimary).lineLimit(1)
                    Image(systemName: "chevron.right").font(.caption2).foregroundStyle(Theme.textTertiary)
                }
                .frame(minHeight: 44)
            }

            if rows.isEmpty {
                Text("No off-peak windows yet. Hours outside any window use your home rate.").themeText(.caption, color: Theme.textTertiary)
            }
            ForEach(Array(rows.enumerated()), id: \.element.id) { i, row in
                windowCard(i, row)
            }

            if !rows.isEmpty && !bad { strips }

            HStack(spacing: 12) {
                Button("Add a window") { rows.append(OffpeakForm.Draft()); model.clearStatus() }
                    .buttonStyle(ThemeSecondaryButtonStyle())
                    .disabled(rows.count >= OffpeakForm.maxWindows)
                if rows.count >= OffpeakForm.maxWindows {
                    Text("Up to \(OffpeakForm.maxWindows) windows.").themeText(.caption, color: Theme.textTertiary)
                }
            }
            Button {
                focused = false
                onSave(OffpeakPut(tz: tz, windows: OffpeakForm.windows(rows)))
            } label: {
                Text(model.saveState == .saving ? "Saving…" : "Save schedule").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeButtonStyle())
            .disabled(bad || !dirty || model.saveState == .saving)
            if bad { Text("Fix the marked fields to save.").themeText(.subline, color: Theme.estimate) }

            SaveStatusView(model: model, savedText: { _ in "Saved." }, webURL: ChargingWebLink.url(serverURL, anchor: "offpeak"), onReload: onReload)

            if !rows.isEmpty && !bad {
                Text(rows.map { "\(OffpeakForm.describe($0)) at \($0.rate.isEmpty ? "0" : $0.rate) \(currency)/kWh" }.joined(separator: " · "))
                    .themeText(.caption, color: Theme.textTertiary)
            }
        }
    }

    // MARK: A window

    @ViewBuilder
    private func windowCard(_ i: Int, _ row: OffpeakForm.Draft) -> some View {
        let e = errs[i]
        let bind = $rows[i]
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text("Window \(i + 1)").themeText(.value, color: Theme.textPrimary)
                Spacer()
                Button("Remove") { rows.remove(at: i); model.clearStatus() }
                    .font(Theme.sans(ThemeTokens.typeSubline, .regular)).foregroundStyle(Theme.textSecondary)
                    .accessibilityLabel("Remove window \(i + 1)")
            }
            LabeledBox(title: "Days") {
                Picker("Days", selection: bind.days) {
                    ForEach(OffpeakForm.Days.allCases) { Text($0.title).tag($0) }
                }
                .pickerStyle(.menu).tint(Theme.textPrimary).frame(minHeight: 44)
            }
            HStack(alignment: .bottom, spacing: 16) {
                LabeledBox(title: "From") { timePicker("From", bind.start) }
                LabeledBox(title: "To") { timePicker("To", bind.end) }
            }
            if let s = OffpeakForm.minutes(row.start), let t = OffpeakForm.minutes(row.end), t < s {
                Text("Ends the next morning.").themeText(.caption, color: Theme.textTertiary)
            }
            LabeledBox(title: "Rate per kWh (\(currency))") {
                TextField("0.09", text: bind.rate).keyboardType(.decimalPad).focused($focused).settingsFieldBox()
                    .accessibilityLabel("Rate per kilowatt hour, window \(i + 1)")
            }
            LabeledBox(title: "Name (optional)") {
                TextField("Overnight", text: bind.label).focused($focused).settingsFieldBox()
                    .accessibilityLabel("Name, window \(i + 1)")
            }
            ForEach([e.rate, e.time, e.label, e.overlap].compactMap { $0 }, id: \.self) {
                Text($0).themeText(.subline, color: Theme.estimate)
            }
        }
        .padding(3 * Theme.unit)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .onChange(of: row) { _, _ in model.clearStatus() }
    }

    private func timePicker(_ title: String, _ time: Binding<String>) -> some View {
        DatePicker(title, selection: Binding(get: { OffpeakForm.date(time.wrappedValue) }, set: { time.wrappedValue = OffpeakForm.time($0) }),
                   displayedComponents: .hourAndMinute)
            .labelsHidden()
            .environment(\.calendar, OffpeakForm.utc)
            .environment(\.timeZone, OffpeakForm.utc.timeZone)
            .frame(minHeight: 44)
    }

    // MARK: Hours strips

    private var strips: some View {
        VStack(alignment: .leading, spacing: 6) {
            strip("Weekdays", .weekdays)
            strip("Weekends", .weekends)
            Text(OffpeakForm.stripNote).themeText(.caption, color: Theme.textTertiary)
        }
    }

    private func strip(_ label: String, _ kind: OffpeakForm.Kind) -> some View {
        let cov = OffpeakForm.coverage(rows, kind)
        return HStack(spacing: 8) {
            Text(label).themeText(.caption, color: Theme.textSecondary).frame(width: 72, alignment: .leading)
            HStack(spacing: 0) {
                ForEach(0..<24, id: \.self) { h in Rectangle().fill(cov[h] ? Theme.accent.opacity(0.7) : Color.clear) }
            }
            .frame(height: 12)
            .background(Theme.border)
            .clipShape(RoundedRectangle(cornerRadius: 3))
            .accessibilityElement()
            .accessibilityLabel(OffpeakForm.spokenCoverage(label, cov))
        }
    }
}

/// The zones the system knows, with a search box; the one chosen is marked.
struct TimeZonePickerView: View {
    @Binding var selection: String
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""

    var body: some View {
        let all = OffpeakForm.zones(including: selection)
        let shown = query.isEmpty ? all : all.filter { $0.localizedCaseInsensitiveContains(query) }
        List(shown, id: \.self) { z in
            Button {
                selection = z
                dismiss()
            } label: {
                HStack {
                    Text(z).themeText(.value, color: Theme.textPrimary)
                    Spacer()
                    if z == selection { Image(systemName: "checkmark").foregroundStyle(Theme.accent) }
                }
            }
            .listRowBackground(Theme.card)
        }
        .scrollContentBackground(.hidden)
        .themePage()
        .searchable(text: $query, prompt: "Search time zones")
        .navigationTitle("Time zone")
        .navigationBarTitleDisplayMode(.inline)
    }
}
