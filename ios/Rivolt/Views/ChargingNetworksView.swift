import SwiftUI

/// The price book of public charging networks, read and changed on the phone: a guest rate, a member rate and plan per network, a
/// preferred flag for the planner, and networks of the person's own. The web's "Charging networks" card with its labels, and under it the
/// networks' own membership terms. A save replaces the list and the server's answer is what is shown as saved. Pricing earlier sessions at
/// a saved rate stays on the web.
struct ChargingNetworksView: View {
    @Environment(AppState.self) private var state
    @State private var model = SettingsCardModel<[ChargingNetwork]>()
    @State private var terms: [MembershipTerm] = []

    var body: some View {
        SettingsStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    ThemeSection(title: "Charging networks") {
                        CardLoadView(model: model, what: "the networks", onReload: reload) { saved in
                            ChargingNetworksEditor(saved: saved, model: model, serverURL: state.serverURL, onSave: { rows in
                                Task { await model.save { try await state.client.setChargingNetworks(rows) } }
                            }, onReset: {
                                Task { await model.save { try await state.client.resetChargingNetworks() } }
                            }, onReload: reload)
                                .id(saved)
                        }
                    }
                    MembershipTermsSection(terms: terms)
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Charging networks")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
        .task {
            await model.load { try await state.client.chargingNetworks() }
            // Hidden when the terms can't be read or there are none, as on the web.
            terms = (try? await state.client.membershipTerms()) ?? []
        }
    }

    private func reload() { Task { await model.load { try await state.client.chargingNetworks() } } }
}

struct ChargingNetworksEditor: View {
    let saved: [ChargingNetwork]
    let model: SettingsCardModel<[ChargingNetwork]>
    let serverURL: URL
    let onSave: ([ChargingNetwork]) -> Void
    let onReset: () -> Void
    let onReload: () -> Void

    @State private var rows: [ChargingNetworksForm.Draft]
    @State private var confirmReset = false
    @State private var formError: String?
    @FocusState private var focused: Bool

    init(saved: [ChargingNetwork], model: SettingsCardModel<[ChargingNetwork]>, serverURL: URL,
         onSave: @escaping ([ChargingNetwork]) -> Void, onReset: @escaping () -> Void, onReload: @escaping () -> Void) {
        self.saved = saved; self.model = model; self.serverURL = serverURL
        self.onSave = onSave; self.onReset = onReset; self.onReload = onReload
        _rows = State(initialValue: ChargingNetworksForm.drafts(saved))
    }

    private var order: [Int] { ChargingNetworksForm.displayOrder(rows.map(\.net)) }
    private var dirty: Bool { ChargingNetworksForm.changed(rows, saved: saved) }
    private var webURL: URL? { ChargingWebLink.url(serverURL, anchor: "networks") }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            if rows.isEmpty {
                Text(ChargingNetworksForm.emptyNote).themeText(.caption, color: Theme.textTertiary)
            }
            ForEach(order, id: \.self) { i in
                if rows.indices.contains(i) { networkCard(i) }
            }
            HStack(spacing: 12) {
                Button("+ Add network") {
                    rows.append(.init(ChargingNetworksForm.newRow(currency: saved.first?.currency ?? "USD")))
                    model.clearStatus()
                }
                .buttonStyle(ThemeSecondaryButtonStyle())
                Button("Reset to defaults") { confirmReset = true }
                    .buttonStyle(ThemeSecondaryButtonStyle())
                    .disabled(model.saveState == .saving)
            }
            Button {
                focused = false
                switch ChargingNetworksForm.prepared(rows) {
                case .success(let list): formError = nil; onSave(list)
                case .failure(let e): formError = e.message
                }
            } label: {
                Text(model.saveState == .saving ? "Saving…" : "Save").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeButtonStyle())
            .disabled(!dirty || model.saveState == .saving)

            if let formError { Text(formError).themeText(.subline, color: Theme.estimate) }
            Text(ChargingNetworksForm.explanation).themeText(.caption, color: Theme.textTertiary)
            SaveStatusView(model: model, savedText: { _ in "Saved." }, webURL: webURL, onReload: onReload)
        }
        .confirmationDialog(ChargingNetworksForm.resetWarning, isPresented: $confirmReset, titleVisibility: .visible) {
            Button("Reset to defaults", role: .destructive) { formError = nil; onReset() }
            Button("Cancel", role: .cancel) {}
        }
    }

    @ViewBuilder
    private func networkCard(_ i: Int) -> some View {
        let bind = $rows[i]
        let r = rows[i]
        let net = r.net
        let custom = (net.slug ?? "").isEmpty
        let preferred = net.preferred == true
        let hasMembership = ChargingNetworksForm.hasMembership(net)
        let saving = ChargingNetworksForm.memberSaving(net)
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                TextField("Network name", text: bind.net.name).focused($focused).settingsFieldBox()
                    .accessibilityLabel("Network name")
                Button { rows.remove(at: i); model.clearStatus() } label: {
                    Image(systemName: "xmark").frame(width: 44, height: 44)
                }
                .foregroundStyle(Theme.textSecondary)
                .accessibilityLabel("Remove \(net.name.isEmpty ? "this network" : net.name)")
            }
            HStack(alignment: .bottom, spacing: 12) {
                LabeledBox(title: "Guest") {
                    HStack(spacing: 4) {
                        TextField("0.42", text: bind.guest).keyboardType(.decimalPad).focused($focused).settingsFieldBox()
                            .accessibilityLabel("Guest rate per kilowatt hour")
                        Text("/kWh").themeText(.caption, color: Theme.textTertiary)
                    }
                }
                if hasMembership {
                    LabeledBox(title: "Member") {
                        HStack(spacing: 4) {
                            TextField("0.36", text: bind.member).keyboardType(.decimalPad).focused($focused).settingsFieldBox()
                                .accessibilityLabel("Member rate per kilowatt hour")
                            Text("/kWh").themeText(.caption, color: Theme.textTertiary)
                        }
                    }
                }
                LabeledBox(title: "Cur") {
                    TextField("USD", text: bind.net.currency).textInputAutocapitalization(.characters).autocorrectionDisabled()
                        .focused($focused).settingsFieldBox().frame(maxWidth: 80)
                        .accessibilityLabel("Currency")
                }
            }
            if saving > 0 {
                Text(ChargingNetworksForm.savingText(saving, currency: net.currency)).themeText(.caption, color: Theme.accent)
            }
            if !r.rateEntered {
                Text(ChargingNetworksForm.unsetRateNote).themeText(.caption, color: Theme.textSecondary)
            }
            if !custom, let saved = saved.first(where: { $0.slug == net.slug }), ChargingNetworksForm.rateSet(saved), let u = webURL {
                VStack(alignment: .leading, spacing: 2) {
                    Link(destination: u) {
                        Text("Price earlier sessions on the web ↗").themeText(.subline, color: Theme.accent).fontWeight(.medium)
                    }
                    Text("Estimated and unpriced fast sessions only; never a price you entered, a bill or a receipt.")
                        .themeText(.caption, color: Theme.textTertiary)
                }
            }
            if custom {
                LabeledBox(title: "Location (optional)") {
                    TextField("40.7128, -74.0060", text: bind.location).keyboardType(.numbersAndPunctuation).focused($focused)
                        .settingsFieldBox().accessibilityLabel("Location, latitude and longitude")
                }
                if case .invalid(let why) = ChargingNetworksForm.parseLocation(r.location) {
                    Text(why).themeText(.subline, color: Theme.estimate)
                } else {
                    Text(ChargingNetworksForm.locationHint).themeText(.caption, color: Theme.textTertiary)
                }
            }
            if hasMembership {
                Toggle(isOn: Binding(get: { net.memberActive == true }, set: { rows[i].net.memberActive = $0 })) {
                    Text("I have \(net.memberPlan ?? "this plan")").themeText(.value, color: Theme.textPrimary)
                }
                .tint(Theme.accent)
                .disabled(!r.memberEntered)
                if !custom {
                    Toggle(isOn: Binding(get: { preferred }, set: { rows[i].net.preferred = $0 })) {
                        Text("\(preferred ? "★" : "☆") Prefer in planner").themeText(.value, color: Theme.textPrimary)
                    }
                    .tint(Theme.accent)
                }
            }
        }
        .padding(3 * Theme.unit)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous)
            .strokeBorder(preferred ? Theme.accent.opacity(0.6) : Theme.border, lineWidth: 1))
        .onChange(of: rows[i]) { _, _ in model.clearStatus() }
    }
}

/// What each network says its membership plans cost and include, from the network's own page, each with where it was read and when it
/// was last checked. Not the person's prices. Hidden when there are no rows.
struct MembershipTermsSection: View {
    let terms: [MembershipTerm]

    var body: some View {
        let groups = ChargingNetworksForm.groupByNetwork(terms)
        if !groups.isEmpty {
            ThemeSection(title: ChargingNetworksForm.termsTitle) {
                Text(ChargingNetworksForm.termsNote).themeText(.caption, color: Theme.textTertiary)
                ForEach(groups, id: \.slug) { g in
                    VStack(alignment: .leading, spacing: 8) {
                        Text(g.network).themeText(.value, color: Theme.textPrimary).accessibilityAddTraits(.isHeader)
                        ForEach(Array(g.plans.enumerated()), id: \.offset) { _, p in
                            VStack(alignment: .leading, spacing: 2) {
                                Text(p.plan).themeText(.value, color: Theme.textPrimary)
                                Text(p.terms).themeText(.subline, color: Theme.textSecondary)
                                HStack(spacing: 4) {
                                    Text(ChargingNetworksForm.readLine(p)).themeText(.caption, color: Theme.textTertiary)
                                    if let u = ChargingNetworksForm.safeSourceURL(p.sourceUrl) {
                                        Link("Source", destination: u).font(Theme.sans(ThemeTokens.typeCaption, .regular))
                                    }
                                }
                            }
                            .padding(.vertical, 4)
                            .accessibilityElement(children: .combine)
                        }
                    }
                }
            }
        }
    }
}
