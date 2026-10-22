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
    @State private var extras = ExtrasModel()
    @State private var session: LiveSession?
    @State private var liveDrive: LiveDrive?
    @State private var latestCharge: ChargeBrief?
    @State private var latestDrive: DriveBrief?
    @State private var charts = ChartsModel()
    @State private var month = PeriodMonth(containing: .now, calendar: .current)
    @Environment(\.tabActive) private var tabActive
    @State private var places = PlacesModel()
    @State private var kpi = KpiModel()
    @State private var recentCharges: [ChargeBrief] = []

    private var selected: Vehicle? {
        vehicles.first { $0.id == selectedID } ?? vehicles.first
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                Group {
                    if let vehicle = selected, vehicleState != nil || now != nil || !isLoading {
                        let summary = NowSummary(state: vehicleState, now: now, clock: .now)
                        let haveData = vehicleState != nil || now != nil
                        VStack(spacing: 16) {
                            if let m = extras.current(vehicleID: vehicle.id) {
                                MilestoneCard(milestone: m, failed: extras.dismissFailed) {
                                    Task { await extras.dismiss(m, source: state.client) }
                                }
                            }
                            threeThings
                            // The live reads (cost so far, the trip) only on fresh data: a stale "Charging" would claim a
                            // charge we can't see.
                            let fresh = summary.freshness != .stale
                            if haveData {
                                NowCard(vehicle: vehicle, summary: summary, place: ChargingPlace.label(summary: summary, charge: latestCharge),
                                        session: fresh ? session : nil, liveDrive: fresh ? liveDrive : nil)
                                ForEach(NowCard.alerts(for: summary, vehicleName: vehicle.name)) { NowAlertBanner(alert: $0) }
                            } else {
                                // Nothing from the truck yet: say so, and still show the month's figures below, as the web does.
                                NoDataCard(vehicleName: vehicle.name)
                            }
                            KpiGrid(tiles: kpiTiles(summary), idle: IdleAwakeTile.make(sleep: charts.sleep, now: .now, calendar: .current),
                                    idleLoading: charts.isLoading)
                            // The web's order after the tiles: where it charges, then the two charts.
                            if let split = places.split, split.total > 0 {
                                PlacesCard(split: split, periodTitle: month.title(calendar: .current))
                            }
                            ChartsSection(model: charts, month: month, limit: vehicleState?.chargeTargetPct)
                        }
                    } else if isLoading {
                        ProgressView().controlSize(.large).padding(.top, 80)
                    } else if let loadError {
                        VStack(spacing: 8) {
                            Image(systemName: "exclamationmark.triangle").font(.title)
                            Text(loadError).multilineTextAlignment(.center)
                        }
                        .foregroundStyle(Theme.textSecondary)
                        .padding(.top, 80)
                    } else if vehicles.isEmpty {
                        Text("No vehicles on this account yet. Connect your Rivian account on the web.")
                            .themeText(.value, color: Theme.textSecondary)
                            .multilineTextAlignment(.center)
                            .padding(.top, 80)
                    }
                }
                .padding(.horizontal, 4 * Theme.unit)
                .padding(.vertical, 4 * Theme.unit)
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .toolbar(.hidden, for: .navigationBar)
            .refreshable { await load(force: true) }
            .task { await load() }
            // While the screen is up the live reads follow the charge or drive (about every 10 s).
            .task(id: "\(selectedID)|\(tabActive)") { if tabActive { await pollLive() } }
            // Charts: from the in-memory cache unless the truck or period changed (pull-to-refresh forces a read).
            .task(id: "\(selected?.id ?? "")|\(month.start.timeIntervalSince1970)|\(lastChargeEnd?.timeIntervalSince1970 ?? 0)") { await loadCharts(force: false) }
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
            .safeAreaInset(edge: .top, spacing: 0) {
                VStack(spacing: 0) {
                    ThemeTopBar { AccountMenu(vehicleID: selected?.id, onRefresh: { Task { await load(force: true) } }) }
                    titleRow
                }
            }
            .sheet(isPresented: $showNotifications) {
                NotificationSettingsView(manager: state.notifications, liveActivitiesAllowed: state.liveActivity.allowedInSettings, live: state.liveActivity, vehicleID: selected?.id)
            }
        }
    }

    /// The page title with the month stepper (and, with more than one truck, which one is shown), pinned under the top
    /// bar like the web's.
    private var titleRow: some View {
        HStack(alignment: .center, spacing: 8) {
            Text("Now").themeText(.hero).accessibilityAddTraits(.isHeader)
            Spacer(minLength: 8)
            if vehicles.count > 1 {
                Menu {
                    Picker("Vehicle", selection: $selectedID) {
                        ForEach(vehicles) { v in Text(v.name).tag(v.id) }
                    }
                } label: {
                    Image(systemName: "car.2").font(.body).foregroundStyle(Theme.textSecondary)
                        .frame(width: 36, height: 36)
                }
                .accessibilityLabel("Switch vehicle")
            }
            ThemeStepper(title: month.title(calendar: .current), canGoForward: !month.isCurrent(now: .now, calendar: .current),
                         previous: { month = month.shifted(by: -1, calendar: .current) },
                         next: { month = month.shifted(by: 1, calendar: .current) })
        }
        .padding(.horizontal, 4 * Theme.unit)
        .padding(.vertical, 2 * Theme.unit)
        .background(Theme.background)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.border).frame(height: 1) }
    }

    /// When the newest finished charge ended (the one in progress doesn't count), for "Since last charge".
    private var lastChargeEnd: Date? {
        KpiLogic.lastFinishedChargeEnd(recentCharges, activeStart: now?.charge?.startedAt)
    }

    private func kpiTiles(_ summary: NowSummary) -> [KpiTile] {
        let drives = KpiLogic.drives(kpi.activity?.drives ?? [])
        let charges = KpiLogic.charges(kpi.activity?.charges ?? [])
        return KpiLogic.tiles(drives: drives, charges: charges, since: kpi.since, sinceKnown: lastChargeEnd == nil,
                              ledger: kpi.ledger, ledgerPending: kpi.ledgerPending, metric: NowFormat.usesMetric)
    }

    private func loadCharts(force: Bool) async {
        guard let id = selected?.id else { return }
        async let c: Void = charts.load(source: state.client, vehicleID: id, month: month, force: force)
        async let p: Void = places.load(source: state.client, vehicleID: id, month: month, force: force)
        async let k: Void = kpi.load(source: state.client, vehicleID: id, month: month, scopeLedger: vehicles.count > 1,
                                      lastChargeEnd: lastChargeEnd, force: force)
        _ = await (c, p, k)
    }

    /// `force` is for a pull or the Refresh button: only then are the charts read again before their cache expires.
    private func load(force: Bool = false) async {
        isLoading = true
        defer { isLoading = false }
        loadError = nil
        do {
            vehicles = try await state.client.vehicles()
            if selected == nil || !vehicles.contains(where: { $0.id == selectedID }) {
                selectedID = vehicles.first?.id ?? ""
            }
            await loadSelected()
            if force { await loadCharts(force: true) }
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
            recentCharges = []
            latestDrive = nil
            return
        }
        let client = state.client
        async let st = try? client.vehicleState(id: id)
        async let nw = try? client.vehicleNow(id: id)
        async let ch = try? client.recentCharges(vehicleID: id)
        async let dr = try? client.latestDrive(vehicleID: id)
        let (s, n, c, d) = await (st, nw, ch, dr)
        vehicleState = s
        now = n ?? nil
        recentCharges = c ?? []
        latestCharge = recentCharges.first
        latestDrive = d ?? nil
        await loadLive(id: id)
        // The widgets read this truck from this server (never the token: that stays in the Keychain).
        WidgetStore().setSession(vehicleID: id, server: state.serverURL)
        WidgetCenter.shared.reloadAllTimelines()
        if s == nil && now == nil {
            loadError = "Couldn't load this truck's status."
        }
        await syncLiveActivity()
        await extras.load(source: client, vehicleID: id)
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

    /// Only once there is some history: a brand-new account has the first-day cards instead.
    @ViewBuilder
    private var threeThings: some View {
        let pushNeeded = state.notifications.offered && !(state.notifications.isOn && state.notifications.preferences?.onChargingDone == true)
        let rows = (latestDrive != nil || latestCharge != nil) ? extras.rows(pushNeeded: pushNeeded) : []
        if !rows.isEmpty {
            ThreeThingsCard(
                rows: rows,
                webURL: { row in
                    let path = row == .lease ? "/settings#lease" : "/settings?tab=notifications"
                    return URL(string: path, relativeTo: state.serverURL)?.absoluteURL
                },
                onOpenNotifications: { showNotifications = true },
                onHide: { extras.hideThreeThings() })
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
