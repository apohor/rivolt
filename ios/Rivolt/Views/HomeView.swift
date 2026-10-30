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
    /// The re-reads after a drive or charge ends (cancelled when another starts).
    @State private var settleTask: Task<Void, Never>?
    @State private var vehicleState: VehicleState?
    @State private var now: NowSnapshot?
    @State private var loadError: String?
    @State private var isLoading = false
    @State private var showNotifications = false
    @State private var sharingMilestone: Milestone?
    @State private var extras = ExtrasModel()
    @State private var session: LiveSession?
    @State private var liveDrive: LiveDrive?
    @State private var latestCharge: ChargeBrief?
    @State private var latestDrive: DriveBrief?
    @State private var charts = ChartsModel()
    /// The scope Now, Activity and Truck share (see `PeriodControl`).
    private var month: Period { state.period }
    @Environment(\.tabActive) private var tabActive
    @Environment(\.scenePhase) private var scenePhase
    /// When the truck was last read from the server, so "Offline" is said only after a recent read (NowFreshness).
    @State private var freshness = NowFreshness()
    /// Bumped by each read so the card's ages move on without waiting for a change in the data.
    @State private var clockTick = Date()
    @State private var places = PlacesModel()
    @State private var kpi = KpiModel()
    @State private var yourWeek = YourWeekModel()
    @State private var recentCharges: [ChargeBrief] = []
    /// The Rivian connection, read once for the "expires" notice (nothing in the demo).
    @State private var rivian: RivianStatus?

    private var selected: Vehicle? {
        vehicles.first { $0.id == selectedID } ?? vehicles.first
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                Group {
                    if let vehicle = selected, vehicleState != nil || now != nil || !isLoading {
                        let summary = freshness.shown(NowSummary(state: vehicleState, now: now, clock: .now), clock: .now)
                        let haveData = vehicleState != nil || now != nil
                        VStack(spacing: 16) {
                            // The live reads (cost so far, the trip) only on fresh data: a stale "Charging" would claim a
                            // charge we can't see.
                            let fresh = summary.freshness != .stale
                            if let line = RivianExpiryLogic.notice(rivian) { RivianExpiryBanner(text: line, serverURL: state.serverURL) }
                            if haveData {
                                NowCard(vehicle: vehicle, summary: summary, place: ChargingPlace.label(summary: summary, charge: latestCharge),
                                        session: fresh ? session : nil, liveDrive: fresh ? liveDrive : nil, charge: fresh ? latestCharge : nil)
                                ForEach(NowCard.alerts(for: summary, vehicleName: vehicle.name)) { NowAlertBanner(alert: $0) }
                            } else {
                                // Nothing from the truck yet: say so, and still show the month's figures below, as the web does.
                                NoDataCard(vehicleName: vehicle.name)
                            }
                            // The truck comes first (battery, range, charging, status); the asks and the milestone after
                            // it, so they never push it below the fold (the scroll audit: on an SE they did).
                            if !state.isDemo, state.notifications.shouldAskInApp {
                                NotificationPromptCard(manager: state.notifications)
                            }
                            if let m = extras.current(vehicleID: vehicle.id) {
                                MilestoneCard(milestone: m, failed: extras.dismissFailed, onShare: { sharingMilestone = m }, canHide: !state.isDemo) {
                                    Task { await extras.dismiss(m, source: state.client) }
                                }
                            }
                            if !state.isDemo { threeThings }
                            if let w = yourWeek.week { YourWeekStrip(week: w) }
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
            .task { await loadRivian() }
            // App Store screenshots only: the demo server limits heavy reads a minute and Now makes many, so read everything again once
            // the allowance has refilled.
            .task {
                guard StoreShots.name == "now" else { return }
                try? await Task.sleep(nanoseconds: 65_000_000_000)
                await load(force: true)
            }
            // While the screen is up and the app is in front, the truck is read again: every 10 s while charging or
            // driving, every minute otherwise (NowRefresh). The loop starts with a read, so coming back to this tab reads at once.
            .task(id: "\(selectedID)|\(tabActive)|\(scenePhase == .active)") { if tabActive, scenePhase == .active { await pollLive() } }
            // "Offline" waits for a recent read: if the last one is old (the app was away), ask the server once first.
            .task(id: recheckKey) { if recheckKey != nil { await refreshNow() } }
            // Charts: from the in-memory cache unless the truck or period changed (pull-to-refresh forces a read).
            .task(id: "\(selected?.id ?? "")|\(month.key)|\(lastChargeEnd?.timeIntervalSince1970 ?? 0)") { await loadCharts(force: false) }
            .onChange(of: selectedID) { Task { await loadSelected() } }
            // A closed Now screen doesn't keep re-reading after a drive or charge ends.
            .onDisappear { settleTask?.cancel() }
            // A drive or charge just ended: the server saves it a little later, so the lists are read again a few times.
            .onChange(of: NowRefresh.inFlight(now?.state ?? NowSummary.inferState(from: vehicleState))) { was, isNow in
                settleTask?.cancel()
                guard was, !isNow else { return }
                settleTask = Task {
                    await load(force: true)
                    var elapsed: TimeInterval = 0
                    for at in NowRefresh.afterSessionEndsDelays {
                        try? await Task.sleep(for: .seconds(at - elapsed))
                        elapsed = at
                        if Task.isCancelled { return }
                        await load(force: true)
                    }
                }
            }
            // The Live Activity follows the switch: on while charging starts it, off ends it.
            .onChange(of: state.notifications.preferences?.onChargingLive) { Task { await syncLiveActivity() } }
            .safeAreaInset(edge: .top, spacing: 0) {
                VStack(spacing: 0) {
                    ThemeTopBar { AccountMenu(vehicleID: selected?.id, onRefresh: { Task { await load(force: true) } }) }
                    titleRow
                }
            }
            .sheet(item: $sharingMilestone) { m in
                ShareCardSheet(fileName: MoneyShareCard.fileName(kind: "milestone", date: m.achievedAt, calendar: .current)) { _ in
                    MoneyShareCard.milestone(m, calendar: .current)
                }
                .followsAppearance()
            }
            .sheet(isPresented: $showNotifications) {
                NotificationSettingsView(manager: state.notifications, liveActivitiesAllowed: state.liveActivity.allowedInSettings, live: state.liveActivity, vehicleID: selected?.id)
                    .followsAppearance()
            }
        }
    }

    /// The page title with the month stepper (and, with more than one truck, which one is shown), pinned under the top
    /// bar like the web's.
    private var titleRow: some View {
        ThemeTitleRow(title: "Now") {
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
            PeriodControl()
        }
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
        // After the others: the demo server limits heavy reads a minute, and the strip is the least important of them.
        await yourWeek.load(source: state.client, vehicleID: id, force: force)
    }

    /// `force` is for a pull or the Refresh button: only then are the charts read again before their cache expires.
    /// The Rivian connection for the "expires" notice. A failed read keeps what was shown; pull-to-refresh reads it again, so the
    /// notice goes once the person has signed in again on the web.
    private func loadRivian() async {
        guard !state.isDemo, let status = try? await state.client.rivianStatus() else { return }
        rivian = status
    }

    private func load(force: Bool = false) async {
        if force { Task { await loadRivian() } }
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
            loadError = UserFacingError.words(error)
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
        let conn = state.connection
        async let st = NowReads.logged("/state", record: { conn.record(.state, $0) }) { try await client.vehicleState(id: id) }
        async let nw = NowReads.logged("/now", record: { conn.record(.now, $0) }) { try await client.vehicleNow(id: id) }
        async let ch = try? client.recentCharges(vehicleID: id)
        async let dr = try? client.latestDrive(vehicleID: id)
        let (s, n, c, d) = await (st, nw, ch, dr)
        vehicleState = s
        now = n ?? nil
        if s != nil || n != nil { freshness.fetched(at: .now) }
        publishConnection()
        recentCharges = c ?? []
        latestCharge = recentCharges.first
        latestDrive = d ?? nil
        await loadLive(id: id)
        // The widgets read this truck from this server (never the token: that stays in the Keychain). Never for the demo.
        if !state.isDemo {
            WidgetStore().setSession(vehicleID: id, server: state.serverURL)
            WidgetCenter.shared.reloadAllTimelines()
        }
        if s == nil && now == nil {
            loadError = "Couldn't load this truck's status."
        }
        if !state.isDemo { await syncLiveActivity() }
        await extras.load(source: client, vehicleID: id)
    }

    /// The session or drive reads that only make sense while charging or in gear.
    private func loadLive(id: String) async {
        // The demo truck has no live Rivian connection to read.
        if state.isDemo { session = nil; liveDrive = nil; return }
        let kind = now?.state ?? NowSummary.inferState(from: vehicleState)
        session = kind == .charging ? (try? await state.client.liveSession(id: id)) ?? nil : nil
        liveDrive = kind == .driving ? (try? await state.client.liveDrive(id: id)) ?? nil : nil
    }

    /// What the Connection details screen shows about the newest frame and the presence limits, from what this screen holds.
    private func publishConnection() {
        state.connection.update(summary: NowSummary(state: vehicleState, now: now, clock: .now), freshness: freshness)
    }

    /// The state and the live reads, from the server now. True when it answered.
    @discardableResult
    private func refreshNow() async -> Bool {
        guard let id = selected?.id else { return false }
        let client = state.client
        let conn = state.connection
        async let st = NowReads.logged("/state", record: { conn.record(.state, $0) }) { try await client.vehicleState(id: id) }
        async let nw = NowReads.logged("/now", record: { conn.record(.now, $0) }) { try await client.vehicleNow(id: id) }
        let (s, n) = await (st, nw)
        if let s { vehicleState = s }
        if let n { now = n }
        let answered = s != nil || n != nil
        if answered { freshness.fetched(at: .now) }
        publishConnection()
        clockTick = .now
        await loadLive(id: id)
        return answered
    }

    /// The newest frame's time when "Offline" is being held back for a re-read; nil when there is nothing to settle.
    private var recheckKey: Date? {
        guard selected != nil, vehicleState != nil || now != nil || !isLoading else { return nil }
        let summary = NowSummary(state: vehicleState, now: now, clock: .now)
        _ = clockTick
        return freshness.holdsOffline(summary, clock: .now) ? (summary.lastSeen ?? .distantPast) : nil
    }

    private func pollLive() async {
        // Read first when the last read is old (the tab was away), then on the state's own cadence.
        if freshness.lastFetchAt.map({ Date.now.timeIntervalSince($0) > NowFreshness.confirmWindow / 3 }) ?? true { await refreshNow() }
        while !Task.isCancelled {
            let kind = now?.state ?? NowSummary.inferState(from: vehicleState)
            try? await Task.sleep(for: .seconds(NowRefresh.pollInterval(kind)))
            guard !Task.isCancelled, selected?.id != nil else { continue }
            await refreshNow()
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
