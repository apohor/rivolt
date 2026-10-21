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

    private var selected: Vehicle? {
        vehicles.first { $0.id == selectedID } ?? vehicles.first
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                Group {
                    if let vehicle = selected, vehicleState != nil || now != nil {
                        NowCard(vehicle: vehicle,
                                summary: NowSummary(state: vehicleState, now: now, clock: .now))
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
                NotificationSettingsView(manager: state.notifications, liveActivitiesAllowed: state.liveActivity.allowedInSettings)
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
            return
        }
        let client = state.client
        async let st = try? client.vehicleState(id: id)
        async let nw = try? client.vehicleNow(id: id)
        let (s, n) = await (st, nw)
        vehicleState = s
        now = n ?? nil
        // The widgets read this truck from this server (never the token: that stays in the Keychain).
        WidgetStore().setSession(vehicleID: id, server: state.serverURL)
        WidgetCenter.shared.reloadAllTimelines()
        if s == nil && now == nil {
            loadError = "Couldn't load this truck's status."
        }
        await syncLiveActivity()
    }

    /// Starts, updates or ends the charging Live Activity from what was just loaded. Only with the
    /// per-iPhone switch on (and notifications on for this phone).
    private func syncLiveActivity() async {
        let switchOn = state.notifications.isOn && state.notifications.liveOffered && state.notifications.preferences?.onChargingLive == true
        let summary = NowSummary(state: vehicleState, now: now, clock: .now)
        await state.liveActivity.sync(summary: summary, switchOn: switchOn, vehicleID: selected?.id, deviceToken: state.notifications.deviceToken)
    }
}
