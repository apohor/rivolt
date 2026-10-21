import SwiftUI
import WidgetKit

/// Home: the Now card for one truck, with a switcher when the account has
/// more than one. Read-only: the app never sends anything to the truck.
///
/// Refresh: on appear, on pull-to-refresh, and when the selected truck
/// changes. `/state` and `/now` load in parallel; without `/now` (an older
/// server) the card works from `/state` alone.
struct HomeView: View {
    @Environment(AppState.self) private var state

    @AppStorage("rivolt.selected_vehicle") private var selectedID = ""
    @State private var vehicles: [Vehicle] = []
    @State private var vehicleState: VehicleState?
    @State private var now: NowSnapshot?
    @State private var loadError: String?
    @State private var isLoading = false
    @State private var showNotifications = false
    @State private var session: LiveSession?
    @State private var liveDrive: LiveDrive?
    @State private var latestCharge: ChargeBrief?
    @State private var latestDrive: DriveBrief?

    private var selected: Vehicle? {
        vehicles.first { $0.id == selectedID } ?? vehicles.first
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                Group {
                    if let vehicle = selected, vehicleState != nil || now != nil {
                        let summary = NowSummary(state: vehicleState, now: now, clock: .now)
                        VStack(spacing: 16) {
                            // Detail cards only on fresh data: a stale "Charging" would claim a charge we can't see.
                            if summary.freshness != .stale,
                               let charging = ChargingDetail.make(summary: summary, session: session, charge: latestCharge) {
                                ChargingDetailCard(detail: charging)
                            }
                            NowCard(vehicle: vehicle, summary: summary)
                            if summary.freshness != .stale,
                               let drive = DriveDetail.make(summary: summary, live: liveDrive, last: latestDrive) {
                                DriveDetailCard(detail: drive)
                            }
                        }
                    } else if isLoading {
                        ProgressView().controlSize(.large).padding(.top, 80)
                    } else if let loadError {
                        VStack(spacing: 8) {
                            Image(systemName: "exclamationmark.triangle").font(.title)
                            Text(loadError).multilineTextAlignment(.center)
                        }
                        .foregroundStyle(.secondary)
                        .padding(.top, 80)
                    } else if vehicles.isEmpty {
                        Text("No vehicles on this account yet. Connect your Rivian account on the web.")
                            .multilineTextAlignment(.center)
                            .foregroundStyle(.secondary)
                            .padding(.top, 80)
                    }
                }
                .padding()
            }
            .background(Color(.systemGroupedBackground))
            .navigationTitle(selected?.name ?? "Rivolt")
            .toolbar {
                if vehicles.count > 1 {
                    ToolbarItem(placement: .topBarLeading) {
                        Menu {
                            Picker("Vehicle", selection: $selectedID) {
                                ForEach(vehicles) { v in Text(v.name).tag(v.id) }
                            }
                        } label: {
                            Image(systemName: "car.2")
                        }
                        .accessibilityLabel("Switch vehicle")
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Button("Refresh") { Task { await load() } }
                        if state.notifications.offered {
                            Button("Notifications") { showNotifications = true }
                        }
                        Divider()
                        Button("Sign out", role: .destructive) {
                            Task { await state.logout() }
                        }
                    } label: {
                        Image(systemName: "person.crop.circle")
                    }
                }
            }
            .refreshable { await load() }
            .task { await load() }
            // While the screen is up the live reads follow the charge or drive (about every 10 s).
            .task(id: selectedID) { await pollLive() }
            .onChange(of: selectedID) { Task { await loadSelected() } }
            // The Live Activity follows the switch: on while charging starts it, off ends it.
            .onChange(of: state.notifications.preferences?.onChargingLive) { Task { await syncLiveActivity() } }
            .safeAreaInset(edge: .top) {
                if state.notifications.shouldAskInApp {
                    NotificationPromptCard(manager: state.notifications)
                        .padding(.horizontal)
                        .padding(.top, 8)
                }
            }
            .sheet(isPresented: $showNotifications) {
                NotificationSettingsView(manager: state.notifications, liveActivitiesAllowed: state.liveActivity.allowedInSettings, live: state.liveActivity, vehicleID: selected?.id)
            }
        }
    }

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        loadError = nil
        do {
            vehicles = try await state.client.vehicles()
            if selected == nil || !vehicles.contains(where: { $0.id == selectedID }) {
                selectedID = vehicles.first?.id ?? ""
            }
            await loadSelected()
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func loadSelected() async {
        guard let id = selected?.id else {
            vehicleState = nil
            now = nil
            session = nil
            liveDrive = nil
            latestCharge = nil
            latestDrive = nil
            return
        }
        let client = state.client
        async let st = try? client.vehicleState(id: id)
        async let nw = try? client.vehicleNow(id: id)
        async let ch = try? client.latestCharge(vehicleID: id)
        async let dr = try? client.latestDrive(vehicleID: id)
        let (s, n, c, d) = await (st, nw, ch, dr)
        vehicleState = s
        now = n ?? nil
        latestCharge = c ?? nil
        latestDrive = d ?? nil
        await loadLive(id: id)
        // The widgets read this truck from this server (never the token: that stays in the Keychain).
        WidgetStore().setSession(vehicleID: id, server: state.serverURL)
        WidgetCenter.shared.reloadAllTimelines()
        if s == nil && now == nil {
            loadError = "Couldn't load this truck's status."
        }
        await syncLiveActivity()
    }

    /// The session or drive reads that only make sense while charging or in gear.
    private func loadLive(id: String) async {
        let kind = now?.state ?? NowSummary.inferState(from: vehicleState)
        session = kind == .charging ? (try? await state.client.liveSession(id: id)) ?? nil : nil
        liveDrive = kind == .driving ? (try? await state.client.liveDrive(id: id)) ?? nil : nil
    }

    private func pollLive() async {
        while !Task.isCancelled {
            try? await Task.sleep(for: .seconds(10))
            guard !Task.isCancelled, let id = selected?.id else { continue }
            let kind = now?.state ?? NowSummary.inferState(from: vehicleState)
            if kind == .charging || kind == .driving {
                async let st = try? state.client.vehicleState(id: id)
                async let nw = try? state.client.vehicleNow(id: id)
                let (s, n) = await (st, nw)
                if let s { vehicleState = s }
                if let n { now = n }
                await loadLive(id: id)
            }
        }
    }

    /// Starts, updates or ends the charging Live Activity from what was just loaded. Only with the
    /// per-iPhone switch on (and notifications on for this phone).
    private func syncLiveActivity() async {
        let switchOn = state.notifications.isOn && state.notifications.liveOffered && state.notifications.preferences?.onChargingLive == true
        let summary = NowSummary(state: vehicleState, now: now, clock: .now)
        await state.liveActivity.sync(summary: summary, switchOn: switchOn, vehicleID: selected?.id, deviceToken: state.notifications.deviceToken)
    }
}
