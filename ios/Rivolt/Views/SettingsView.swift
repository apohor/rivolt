import SwiftUI

/// What Settings lists and in what order, with no view code so a test can see it. It mirrors the web's Settings tabs and names.
enum SettingsLogic {
    enum Row: String, Equatable {
        case plan, restore, notices, connection, signOut, devices, passkeys, vehicleProfile, primaryVehicle, homeLocation, units, chargingRates, offpeak, chargingNetworks, plannerDefaults, notifications, appearance
        case export, deleteHistory, deleteAccount, privacy, terms, support, acknowledgements

        var title: String {
            switch self {
            case .plan: return "Rivolt Pro"
            case .restore: return "Restore purchases"
            case .notices: return "Notices"
            case .connection: return "Connection details"
            case .signOut: return "Sign out"
            case .devices: return "Signed-in devices"
            case .passkeys: return "Passkeys"
            case .vehicleProfile: return "Vehicle profile"
            case .primaryVehicle: return "Vehicle the free plan shows"
            case .homeLocation: return "Home location"
            case .units: return "Units"
            case .chargingRates: return "Charging rates"
            case .offpeak: return "Off-peak schedule"
            case .chargingNetworks: return "Charging networks"
            case .plannerDefaults: return "Trip planner defaults"
            case .notifications: return "Notifications"
            case .appearance: return "Appearance"
            case .export: return "Export your data"
            case .deleteHistory: return "Delete older history"
            case .deleteAccount: return "Delete account"
            case .privacy: return "Privacy policy"
            case .terms: return "Terms"
            case .support: return "Support"
            case .acknowledgements: return "Acknowledgements"
            }
        }

        var symbol: String {
            switch self {
            case .plan: return "sparkles"
            case .restore: return "arrow.clockwise.circle"
            case .notices: return "bell.badge"
            case .connection: return "antenna.radiowaves.left.and.right"
            case .signOut: return "rectangle.portrait.and.arrow.right"
            case .devices: return "iphone.and.macbook"
            case .passkeys: return "person.badge.key"
            case .vehicleProfile: return "car"
            case .primaryVehicle: return "car.2"
            case .homeLocation: return "house"
            case .units: return "ruler"
            case .chargingRates: return "bolt"
            case .offpeak: return "moon.stars"
            case .chargingNetworks: return "ev.charger"
            case .plannerDefaults: return "map"
            case .notifications: return "bell"
            case .appearance: return "circle.lefthalf.filled"
            case .export: return "square.and.arrow.up"
            case .deleteHistory: return "calendar.badge.minus"
            case .deleteAccount: return "trash"
            case .privacy: return "hand.raised"
            case .terms: return "doc.text"
            case .support: return "questionmark.circle"
            case .acknowledgements: return "heart"
            }
        }
    }

    struct Group: Equatable, Identifiable {
        let title: String
        let rows: [Row]
        var id: String { title }
    }

    /// Account, Security, Vehicle, Charging, Notifications (only where this server can push), App, Data and About, as the web's tabs.
    /// "Rivolt Pro" is listed only where the server bills (the account carries a plan); with billing off nothing mentions a plan.
    static func groups(notificationsOffered: Bool, planOffered: Bool = false, primaryVehicleOffered: Bool = false, noticesOffered: Bool = false, restoreOffered: Bool = false) -> [Group] {
        var out = [
            Group(title: "Account", rows: (planOffered ? [.plan] : []) + (restoreOffered ? [.restore] : []) + (noticesOffered ? [.notices] : []) + [.connection, .signOut]),
            Group(title: "Security", rows: [.devices, .passkeys]),
            Group(title: "Vehicle", rows: [.vehicleProfile] + (planOffered && primaryVehicleOffered ? [.primaryVehicle] : []) + [.homeLocation, .units]),
            Group(title: "Charging", rows: [.chargingRates, .offpeak, .chargingNetworks, .plannerDefaults]),
        ]
        if notificationsOffered { out.append(Group(title: "Notifications", rows: [.notifications])) }
        out += [
            Group(title: "App", rows: [.appearance]),
            Group(title: "Data", rows: [.export, .deleteHistory, .deleteAccount]),
            Group(title: "About", rows: [.privacy, .terms, .support, .acknowledgements]),
        ]
        return out
    }

    /// "Version 0.2.26 (214)", at the bottom.
    static func versionLine(bundle: Bundle = .main) -> String {
        let v = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
        let b = bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "?"
        return "Version \(v) (\(b))"
    }
}

/// Settings: one grouped list, the screens it opens pushed on the same stack (no sheets on sheets), grouped and named as the web's
/// Settings tabs. Opened from the person icon in the top bar.
struct SettingsView: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss
    /// The truck the vehicle and notification settings are for.
    var vehicleID: String?

    @AppStorage(AppearanceChoice.storageKey) private var appearance = AppearanceChoice.system
    @State private var showDelete = false
    @State private var plan: PlanState = .off
    @State private var hasNotices = false
    /// Billing is off here but StoreKit is set up: an existing Apple subscriber can still restore (it sells nothing).
    @State private var restoreOffered = false

    var body: some View {
        NavigationStack {
            List {
                ForEach(SettingsLogic.groups(notificationsOffered: state.notifications.offered, planOffered: plan != .off, primaryVehicleOffered: plan == .free, noticesOffered: hasNotices, restoreOffered: restoreOffered)) { group in
                    Section {
                        ForEach(group.rows, id: \.rawValue) { row in rowView(row) }
                    } header: {
                        Text(group.title).themeText(.label, color: Theme.textTertiary).accessibilityAddTraits(.isHeader)
                    }
                    .listRowBackground(Theme.card)
                }
                Section {
                    Text(SettingsLogic.versionLine()).themeText(.caption, color: Theme.textTertiary)
                        .frame(maxWidth: .infinity)
                }
                .listRowBackground(Color.clear)
            }
            .listStyle(.insetGrouped)
            .scrollContentBackground(.hidden)
            .themePage()
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }.font(Theme.sans(ThemeTokens.typeValue, .medium)).tint(Theme.accent)
                }
            }
            .sheet(isPresented: $showDelete) { DeleteAccountSheet(billing: plan != .off).followsAppearance() }
            .task {
                guard !state.isDemo else { return }
                plan = PlanLogic.state(try? await state.client.me())
                hasNotices = !((try? await state.client.notices()) ?? []).isEmpty
                if plan == .off, let config = try? await state.client.appConfig() { restoreOffered = config.restoreAvailable }
            }
        }
        .environment(\.inSettings, true)
    }

    private func label(_ row: SettingsLogic.Row, color: Color = Theme.textPrimary) -> some View {
        Label {
            Text(row.title).themeText(.value, color: color)
        } icon: {
            Image(systemName: row.symbol).foregroundStyle(color == Theme.textPrimary ? Theme.textSecondary : color)
        }
    }

    @ViewBuilder
    private func rowView(_ row: SettingsLogic.Row) -> some View {
        switch row {
        case .plan:
            NavigationLink { ProPaywallView(plan: plan) } label: { label(row) }
        case .restore:
            NavigationLink { RestorePurchasesView() } label: { label(row) }
        case .notices:
            NavigationLink { NoticesView() } label: { label(row) }
        case .connection:
            NavigationLink { ConnectionDetailsView(diagnostics: state.connection, serverURL: state.serverURL) } label: { label(row) }
        case .signOut:
            Button(role: .destructive) { dismiss(); Task { await state.logout() } } label: { label(row, color: Theme.critical) }
        case .devices:
            NavigationLink { DevicesView() } label: { label(row) }
        case .passkeys:
            NavigationLink { PasskeysView() } label: { label(row) }
        case .vehicleProfile:
            NavigationLink { VehicleProfileView(vehicleID: vehicleID) } label: { label(row) }
        case .primaryVehicle:
            NavigationLink { PrimaryVehicleView() } label: { label(row) }
        case .homeLocation:
            NavigationLink { HomeLocationView(vehicleID: vehicleID) } label: { label(row) }
        case .units:
            NavigationLink { UnitsView() } label: { label(row) }
        case .chargingRates:
            NavigationLink { ChargingRatesView() } label: { label(row) }
        case .offpeak:
            NavigationLink { OffpeakView() } label: { label(row) }
        case .chargingNetworks:
            NavigationLink { ChargingNetworksView() } label: { label(row) }
        case .plannerDefaults:
            NavigationLink { PlannerDefaultsView() } label: { label(row) }
        case .notifications:
            NavigationLink {
                NotificationSettingsView(manager: state.notifications, liveActivitiesAllowed: state.liveActivity.allowedInSettings,
                                         live: state.liveActivity, vehicleID: vehicleID)
            } label: { label(row) }
        case .appearance:
            Picker(selection: $appearance) {
                ForEach(AppearanceChoice.allCases) { Text($0.title).tag($0) }
            } label: { label(row) }
            .pickerStyle(.menu)
            .tint(Theme.textSecondary)
        case .export:
            NavigationLink { ExportDataView() } label: { label(row) }
        case .deleteHistory:
            // Irreversible, so web only: it opens the web page, where the confirmation lives (the app token cannot do it).
            VStack(alignment: .leading, spacing: 4) {
                Link(destination: DeleteHistoryLink.url(state.serverURL)) { label(row) }
                Text(DeleteHistoryLink.caption(state.serverURL)).themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
            }
        case .deleteAccount:
            Button(role: .destructive) { showDelete = true } label: { label(row, color: Theme.critical) }
        case .privacy:
            Link(destination: state.serverURL.appending(path: "privacy")) { label(row) }
        case .terms:
            Link(destination: state.serverURL.appending(path: "terms")) { label(row) }
        case .support:
            Link(destination: state.serverURL.appending(path: "support")) { label(row) }
        case .acknowledgements:
            NavigationLink { AcknowledgementsView() } label: { label(row) }
        }
    }
}

/// The passkeys on the account, read only: the list and the way to add or remove one (on the web).
struct PasskeysView: View {
    @Environment(AppState.self) private var state
    @State private var model = PasskeysModel()

    var body: some View {
        SettingsStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    PasskeysSection(model: model, serverURL: state.serverURL, onReload: { Task { await model.load(source: state.client) } })
                    if model.phase == .off {
                        Text("Passkeys aren't turned on for this server.").themeText(.value, color: Theme.textSecondary)
                    }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Passkeys")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
        .task { await model.load(source: state.client) }
    }
}
