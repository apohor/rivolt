import SwiftUI

/// The three screens behind the account menu's "Home location", "Vehicle profile" and "Export your data". They share the look of
/// Charging rates: a sheet with a Done button over a themed page.
private struct SettingsSheet<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) { content }
                    .padding(4 * Theme.unit)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .themePage()
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }.font(Theme.sans(ThemeTokens.typeValue, .medium)).tint(Theme.accent)
                }
            }
        }
    }
}

// MARK: Home location

struct HomeLocationView: View {
    @Environment(AppState.self) private var state
    /// The truck to take the position from; nil falls back to the account's first truck.
    var vehicleID: String?
    @State private var model = HomeLocationModel()
    @State private var query = ""
    @State private var confirmClear = false
    @State private var truckID: String?

    var body: some View {
        SettingsSheet(title: "Home location") {
            HomeLocationContent(model: model, query: $query, canUseTruck: truckID != nil,
                                onSearch: { Task { await model.search(source: state.client, query: query) } },
                                onPick: { place in Task { await model.setHome(source: state.client, place: place) } },
                                onUseTruck: { if let id = truckID { Task { await model.setHomeToTruck(source: state.client, vehicleID: id) } } },
                                onRestore: { Task { await model.restore(source: state.client) } },
                                onClear: { confirmClear = true },
                                onReload: { Task { await model.load(source: state.client) } })
        }
        .confirmationDialog("Clear your home?", isPresented: $confirmClear, titleVisibility: .visible) {
            Button("Clear home", role: .destructive) { Task { await model.clear(source: state.client) } }
            Button("Keep it", role: .cancel) {}
        } message: {
            Text("Charges are labelled Home or Away by it, and home charging is priced from it. You can restore it afterwards.")
        }
        .task {
            await model.load(source: state.client)
            if let vehicleID { truckID = vehicleID } else { truckID = (try? await state.client.vehicles())?.first?.id }
        }
    }
}

struct HomeLocationContent: View {
    let model: HomeLocationModel
    @Binding var query: String
    var canUseTruck: Bool
    let onSearch: () -> Void
    let onPick: (PlaceMatch) -> Void
    let onUseTruck: () -> Void
    let onRestore: () -> Void
    let onClear: () -> Void
    let onReload: () -> Void
    @FocusState private var focused: Bool

    var body: some View {
        switch model.loadState {
        case .loading:
            ProgressView().frame(maxWidth: .infinity).padding(.top, 8 * Theme.unit)
        case .failed:
            Text("Couldn't load your home.").themeText(.value, color: Theme.estimate)
            Button("Try again", action: onReload).buttonStyle(ThemeSecondaryButtonStyle())
        case .loaded(let home):
            current(home)
            Button(action: onUseTruck) { Text("Use where the truck is").frame(maxWidth: .infinity) }
                .buttonStyle(ThemeButtonStyle())
                .disabled(!canUseTruck || model.busy)
            if home.canSearchAddress { search }
            else { Text("This server has no address search. Use where the truck is, or set a home on the web.").themeText(.subline, color: Theme.textTertiary) }
            if !model.results.isEmpty { results }
            if home.canRestore {
                Button(action: onRestore) { Text("Restore the earlier home").frame(maxWidth: .infinity) }
                    .buttonStyle(ThemeSecondaryButtonStyle()).disabled(model.busy)
            }
            if home.set {
                Button(role: .destructive, action: onClear) { Text("Clear home").frame(maxWidth: .infinity) }
                    .buttonStyle(ThemeSecondaryButtonStyle()).disabled(model.busy)
            }
            if let m = model.message {
                Text(m).themeText(.subline, color: model.messageIsError ? Theme.estimate : Theme.textBody).fixedSize(horizontal: false, vertical: true)
            }
            Text("Rivolt labels charges Home or Away by it and prices home charging from it. The app never reads this phone's location; a home is set from an address you type or from where the truck says it is.")
                .themeText(.caption, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
        }
    }

    private func current(_ home: HomeLocation) -> some View {
        VStack(alignment: .leading, spacing: Theme.unit) {
            Text("Home").themeText(.label)
            if home.set {
                Text(home.address.flatMap { $0.isEmpty ? nil : $0 } ?? home.label ?? "Home").themeText(.value, color: Theme.textPrimary)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                Text("Not set").themeText(.value, color: Theme.textSecondary)
            }
        }
    }

    private var search: some View {
        VStack(alignment: .leading, spacing: 2 * Theme.unit) {
            Text("Search an address").themeText(.label)
            TextField("Street, number and city", text: $query)
                .textContentType(.fullStreetAddress).submitLabel(.search).focused($focused)
                .onSubmit { focused = false; onSearch() }
                .padding(.horizontal, 3 * Theme.unit).frame(minHeight: 44)
                .background(Theme.background, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
            Button(action: { focused = false; onSearch() }) { Text(model.busy ? "Searching…" : "Search").frame(maxWidth: .infinity) }
                .buttonStyle(ThemeSecondaryButtonStyle())
                .disabled(query.trimmingCharacters(in: .whitespaces).count < 3 || model.busy)
        }
    }

    private var results: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(model.results) { place in
                Button { onPick(place) } label: {
                    Text(place.line).themeText(.value, color: Theme.textPrimary).multilineTextAlignment(.leading)
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                }
                .buttonStyle(.plain)
                .disabled(model.busy)
                Divider().overlay(Theme.border)
            }
        }
    }
}

// MARK: Vehicle profile

struct VehicleProfileView: View {
    @Environment(AppState.self) private var state
    var vehicleID: String?
    @State private var model = VehicleProfileModel()
    @State private var vehicle: Vehicle?

    var body: some View {
        SettingsSheet(title: "Vehicle profile") {
            if let vehicle {
                switch model.loadState {
                case .loading: ProgressView().frame(maxWidth: .infinity).padding(.top, 8 * Theme.unit)
                case .failed:
                    Text("Couldn't load the profile.").themeText(.value, color: Theme.estimate)
                    Button("Try again") { Task { await model.load(source: state.client, vehicleID: vehicle.id) } }.buttonStyle(ThemeSecondaryButtonStyle())
                case .loaded(let profile):
                    VehicleProfileForm(model: model, vehicleName: vehicle.name, initial: profile) { draft in
                        Task { await model.save(source: state.client, vehicleID: vehicle.id, draft) }
                    }
                    .id(profile)
                }
            } else {
                ProgressView().frame(maxWidth: .infinity).padding(.top, 8 * Theme.unit)
            }
        }
        .task {
            let list = (try? await state.client.vehicles()) ?? []
            vehicle = list.first { $0.id == vehicleID } ?? list.first
            if let v = vehicle { await model.load(source: state.client, vehicleID: v.id) }
        }
    }
}

struct VehicleProfileForm: View {
    let model: VehicleProfileModel
    let vehicleName: String
    let onSave: (VehicleProfile) -> Void
    @State private var draft: VehicleProfile
    @State private var load: String
    @State private var placard: String

    init(model: VehicleProfileModel, vehicleName: String, initial: VehicleProfile, onSave: @escaping (VehicleProfile) -> Void) {
        self.model = model
        self.vehicleName = vehicleName
        self.onSave = onSave
        _draft = State(initialValue: initial)
        _load = State(initialValue: (initial.defaultExtraLoadLb ?? 0) > 0 ? NumberText.fixed(initial.defaultExtraLoadLb ?? 0, 0) : "")
        _placard = State(initialValue: (initial.tirePlacardPsi ?? 0) > 0 ? NumberText.fixed(initial.tirePlacardPsi ?? 0, 0) : "")
    }

    var body: some View {
        Text(vehicleName).themeText(.label)
        Text("What you tell Rivolt about the truck goes into its efficiency figures and the trip planner. Nothing here is sent to Rivian.")
            .themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
        row("Tires") {
            Picker("Tires", selection: Binding(get: { draft.tireType ?? "" }, set: { draft.tireType = $0.isEmpty ? nil : $0 })) {
                ForEach(VehicleProfileChoices.tires, id: \.value) { Text($0.label).tag($0.value) }
            }
        }
        row("Wheels") {
            Picker("Wheels", selection: Binding(get: { draft.wheelInches ?? 0 }, set: { draft.wheelInches = $0 == 0 ? nil : $0 })) {
                ForEach(VehicleProfileChoices.wheels, id: \.self) { Text($0 == 0 ? "Not set" : "\($0) inch").tag($0) }
            }
        }
        row("Charge port") {
            Picker("Charge port", selection: Binding(get: { VehicleProfileChoices.chargePortValue(draft.nativeNacs) }, set: { draft.nativeNacs = VehicleProfileChoices.nacs(forChargePort: $0) })) {
                ForEach(VehicleProfileChoices.chargePort, id: \.value) { Text($0.label).tag($0.value) }
            }
        }
        Text("Accessories").themeText(.label)
        ForEach(VehicleProfileChoices.accessories, id: \.value) { a in
            Toggle(a.label, isOn: Binding(
                get: { draft.accessories?.contains(a.value) == true },
                set: { on in
                    var list = draft.accessories ?? []
                    list.removeAll { $0 == a.value }
                    if on { list.append(a.value) }
                    draft.accessories = list.isEmpty ? nil : list
                }))
                .themeText(.value, color: Theme.textPrimary).tint(Theme.accent).frame(minHeight: 44)
        }
        Toggle("Often pulls a trailer", isOn: Binding(get: { draft.frequentlyTows == true }, set: { draft.frequentlyTows = $0 ? true : nil }))
            .themeText(.value, color: Theme.textPrimary).tint(Theme.accent).frame(minHeight: 44)
        number("Usual extra load (lb)", text: $load)
        number("Tire pressure on the door-jamb placard (psi)", text: $placard)
        Button(action: save) { Text(model.saveState == .saving ? "Saving…" : "Save").frame(maxWidth: .infinity) }
            .buttonStyle(ThemeButtonStyle()).disabled(model.saveState == .saving)
        switch model.saveState {
        case .saved: Text("Saved.").themeText(.subline, color: Theme.textBody)
        case .failed(let m): Text(m).themeText(.subline, color: Theme.estimate)
        default: EmptyView()
        }
    }

    private func save() {
        var p = draft
        p.defaultExtraLoadLb = Double(load).flatMap { $0 > 0 ? $0 : nil }
        p.tirePlacardPsi = Double(placard).flatMap { $0 > 0 ? $0 : nil }
        onSave(p)
    }

    private func row<C: View>(_ label: String, @ViewBuilder _ control: () -> C) -> some View {
        HStack {
            Text(label).themeText(.value, color: Theme.textSecondary)
            Spacer(minLength: 8)
            control().pickerStyle(.menu).tint(Theme.textPrimary)
        }
        .frame(minHeight: 44)
    }

    private func number(_ label: String, text: Binding<String>) -> some View {
        VStack(alignment: .leading, spacing: Theme.unit) {
            Text(label).themeText(.label)
            TextField("0", text: text).keyboardType(.decimalPad)
                .padding(.horizontal, 3 * Theme.unit).frame(minHeight: 44)
                .background(Theme.background, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
        }
    }
}

// MARK: Export

/// Export is a download of everything the account holds, so it stays on the web, where it is behind the person's own sign-in:
/// the app's token can't pull it (the server refuses it, by design).
struct ExportDataView: View {
    @Environment(AppState.self) private var state

    static func url(server: URL) -> URL? { URL(string: "/settings?tab=data", relativeTo: server)?.absoluteURL }

    var body: some View {
        SettingsSheet(title: "Export your data") {
            Text("Download everything Rivolt holds for you as a zip of CSV files or a full backup. It opens on the web, in your browser, where you're signed in as yourself; the app can't download it for you.")
                .themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
            if let url = Self.url(server: state.serverURL) {
                Link(destination: url) { Text("Open Data settings on the web").frame(maxWidth: .infinity) }
                    .buttonStyle(ThemeButtonStyle())
            }
        }
    }
}
