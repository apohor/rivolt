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
    /// The truck `vehicleState` was read for.
    @State private var stateVehicleID: String?
    @State private var now: NowSnapshot?
    /// The user's own range at today's temperature, for the redesigned hero's "Full charge today" line.
    @State private var coldRange: ColdRange?
    @State private var loadError: String?
    @State private var isLoading = false
    @State private var showNotifications = false
    @State private var sharingMilestone: Milestone?
    @State private var extras = ExtrasModel()
    @State private var session: LiveSession?
    @State private var liveDrive: LiveDrive?
    /// The truck's navigation destination while it drives (design_preview key live_destination); the first 404 turns the asking off for this truck.
    @State private var liveDestination: LiveDestination?
    @State private var destinationOff = false
    @State private var latestCharge: ChargeBrief?
    @State private var latestDrive: DriveBrief?
    @State private var charts = ChartsModel()
    /// The scope Now, Activity and Truck share (see `PeriodControl`).
    private var month: Period { state.period }
    @Environment(\.tabActive) private var tabActive
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.dynamicTypeSize) private var typeSize
    /// When the truck was last read from the server, so "Offline" is said only after a recent read (NowFreshness).
    @State private var freshness = NowFreshness()
    /// Bumped by each read so the card's ages move on without waiting for a change in the data.
    @State private var clockTick = Date()
    @State private var places = PlacesModel()
    @State private var kpi = KpiModel()
    @State private var recentCharges: [ChargeBrief] = []
    /// The Rivian connection, read once for the "expires" notice (nothing in the demo).
    @State private var rivian: RivianStatus?

    /// A locked vehicle (a free account's non-primary) is opened only when picked in this visit.
    @State private var pickedLocked = false

    /// The redesigned Now (design_preview key ios_now_v2, apart from the web's now_v2); off on prod, so the screen is the one it always was.
    @State private var showLive = false
    @State private var destinationFailures = 0
    @State private var driveFailures = 0
    private var v2: Bool { state.designPreview.contains("ios_now_v2") }
    /// The state slot under the gauge (design_preview key now_slot, on the redesign): the map and weather parked, the figures charging or driving.
    /// The calmer Now (design_preview key ios_now_tidy, with the redesign): status inside the card, the destination on Now, asks last.
    private var tidy: Bool { v2 && state.designPreview.contains("ios_now_tidy") }
    private var slotOn: Bool { v2 && state.designPreview.contains("now_slot") }
    /// The charging graph on the Now card (design_preview key now_charge_graph, with the calm Now and the slot; on the web the same key).
    private var chargeGraphOn: Bool { tidy && slotOn && state.designPreview.contains("now_charge_graph") }
    @State private var chargeSeries = NowChargeSeriesModel()
    /// The parked graph on the Now card (design_preview key now_parked_graph, with the calm Now and the slot; on the web the same key).
    private var parkedGraphOn: Bool { tidy && slotOn && state.designPreview.contains("now_parked_graph") }
    @State private var parkedSeries = NowParkedSeriesModel()
    /// A parked, plugged or sleeping truck: the states the graph is for.
    private var parkedKind: Bool {
        let s = NowSummary(state: vehicleState, now: now, clock: .now)
        return [.parked, .plugged, .asleep].contains(s.kind) || s.sleeping
    }
    /// When the open charge began, only while one is open: what the graph reads from.
    private var chargeSampleStart: Date? {
        let s = NowSummary(state: vehicleState, now: now, clock: .now)
        return s.kind == .charging ? s.chargeStartedAt : nil
    }
    private var slotHome: NowSlotModel.Home? {
        guard let h = homeLocation, h.set else { return nil }
        return NowSlotModel.Home(lat: h.latitude, lon: h.longitude, label: h.label.flatMap { $0.isEmpty ? nil : $0 })
    }
    /// The truck's position for the parked map, read only when `/now` has none (it has one for the demo account only).
    /// The saved home, for the slot's "Home" label and charger chips (only with now_slot); read again when the tab or the app comes to the front.
    @State private var homeLocation: HomeLocation?
    /// Counts closings of the Settings sheet; the saved home is read again on each.
    @State private var settingsClosed = 0

    private var selected: Vehicle? {
        VehicleChoice.selected(vehicles, chosen: selectedID, pickedLocked: pickedLocked)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                Group {
                    if let vehicle = selected, vehicle.proLocked == true {
                        // The history of a free account's other vehicle is Pro; it keeps recording. One note, not a screen of refused reads.
                        ProLimitNote(limit: .proRequired, message: ProCopy.vehicleLimit)
                    } else if let vehicle = selected, vehicleState != nil || now != nil || !isLoading {
                        let summary = freshness.shown(NowSummary(state: vehicleState, now: now, clock: .now), clock: .now)
                        let haveData = vehicleState != nil || now != nil
                        VStack(spacing: 16) {
                            // The live reads (cost so far, the trip) only on fresh data: a stale "Charging" would claim a
                            // charge we can't see.
                            let fresh = summary.freshness != .stale
                            if let line = RivianExpiryLogic.notice(rivian) { RivianExpiryBanner(text: line, serverURL: state.serverURL) }
                            PlanNoticeView()
                            ServiceNoticeBanner()
                            VehicleScopeNote(vehicles: vehicles)
                            if haveData {
                                NowCard(vehicle: vehicle, summary: summary, place: ChargingPlace.label(summary: summary, charge: latestCharge),
                                        session: fresh ? session : nil, liveDrive: fresh ? liveDrive : nil, charge: fresh ? latestCharge : nil,
                                        v2: v2, images: vehicle.images, fullChargeRange: v2 ? coldRange?.fullChargeRange : nil,
                                        onLive: v2 ? { showLive = true } : nil,
                                        slotOn: slotOn, snapshot: now, truckFix: vehicleState?.coordinate, home: slotHome,
                                        gear: vehicleState?.gear, driveMode: vehicleState?.driveMode,
                                        tidy: tidy, chip: tidy ? chipText : nil, chipTone: chipTone, destination: fresh ? liveDestination : nil,
                                        chargeSeries: chargeGraphOn && chargeSeries.matches(vehicleID: selected?.id, startedAt: chargeSampleStart) ? chargeSeries : nil, parkedSeries: parkedGraphOn ? parkedSeries : nil)
                                ForEach(NowCard.alerts(for: summary, vehicleName: vehicle.name)) { NowAlertBanner(alert: $0) }
                            } else {
                                // Nothing from the truck yet: say so, and still show the month's figures below, as the web does.
                                NoDataCard(vehicleName: vehicle.name)
                            }
                            // The truck comes first (battery, range, charging, status); the asks and the milestone after
                            // it, so they never push it below the fold (the scroll audit: on an SE they did).
                            if !tidy { asks(vehicle) }
                            if !state.isDemo, !v2 { threeThings }
                            if v2 {
                                NowMonthStrip(figures: NowMonthStripFigure.make(tiles: kpiTiles(summary), metric: NowFormat.usesMetric))
                            } else {
                                KpiGrid(tiles: kpiTiles(summary), idle: IdleAwakeTile.make(sleep: charts.sleep, now: .now, calendar: .current),
                                        idleLoading: charts.isLoading)
                            }
                            // The web's order after the tiles: where it charges, then the two charts.
                            if let split = places.split, split.total > 0 {
                                PlacesCard(split: split, periodTitle: month.title(calendar: .current))
                            }
                            ChartsSection(model: charts, month: month, limit: vehicleState?.chargeTargetPct)
                            // The calmer Now puts the asks and the milestone last too.
                            if tidy { asks(vehicle) }
                            // The redesign puts the three things last, after the figures and charts.
                            if !state.isDemo, v2 { threeThings }
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
            .navigationDestination(isPresented: $showLive) {
                if let vehicle = selected {
                    let summary = freshness.shown(NowSummary(state: vehicleState, now: now, clock: .now), clock: .now)
                    NowLiveView(vehicle: vehicle, summary: summary, session: summary.freshness != .stale ? session : nil,
                                liveDrive: summary.freshness != .stale ? liveDrive : nil, destination: summary.freshness != .stale ? liveDestination : nil, charge: summary.freshness != .stale ? latestCharge : nil,
                                tidy: tidy, images: vehicle.images) { showLive = false }
                        .toolbar(.hidden, for: .navigationBar)
                }
            }
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
            // The saved home (the slot's Home label and its charger chips) is read again each time this tab comes to the front or
            // the app returns or the Settings sheet closes, so a change in Settings shows; a failed read keeps the last one. The demo has a saved home too (its sample place).
            .task(id: "\(slotOn)|\(tabActive)|\(scenePhase == .active)|\(settingsClosed)") {
                guard slotOn, tabActive, scenePhase == .active, let h = try? await state.client.homeLocation() else { return }
                homeLocation = h
            }
            // The open charge's stored samples for the graph, read again every 30 s while this tab and the app are in front.
            .task(id: "\(chargeGraphOn)|\(selectedID)|\(tabActive)|\(scenePhase == .active)|\(chargeSampleStart?.timeIntervalSince1970 ?? 0)") {
                guard chargeGraphOn, tabActive, scenePhase == .active, let id = selected?.id, let started = chargeSampleStart else { return }
                while !Task.isCancelled {
                    await chargeSeries.load(source: state.client, vehicleID: id, startedAt: started)
                    try? await Task.sleep(for: .seconds(NowChargeSeriesModel.refresh))
                }
            }
            // The parked graph's series, read again every minute while this tab and the app are in front and the truck is parked.
            .task(id: "\(parkedGraphOn)|\(parkedKind)|\(selectedID)|\(tabActive)|\(scenePhase == .active)") {
                guard parkedGraphOn, parkedKind, tabActive, scenePhase == .active, let id = selected?.id else { parkedSeries.clear(); return }
                while !Task.isCancelled {
                    await parkedSeries.load(source: state.client, vehicleID: id)
                    try? await Task.sleep(for: .seconds(NowParkedSeriesModel.refresh))
                }
            }
            // "Offline" waits for a recent read: if the last one is old (the app was away), ask the server once first.
            .task(id: recheckKey) { if recheckKey != nil { await refreshNow() } }
            // Charts: from the in-memory cache unless the truck or period changed (pull-to-refresh forces a read).
            .task(id: "\(selected?.id ?? "")|\(month.key)|\(lastChargeEnd?.timeIntervalSince1970 ?? 0)") { await loadCharts(force: false) }
            // The Live screen just opened: read the destination now, not at the next poll.
            .onChange(of: showLive) { if showLive, let id = selected?.id { Task { await loadLive(id: id) } } }
            .onChange(of: selectedID) { destinationOff = false; liveDestination = nil; liveDrive = nil; driveFailures = 0; Task { await loadSelected() } }
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
                    ThemeTopBar { AccountMenu(vehicleID: selected?.id, onRefresh: { Task { await load(force: true) } }, onSettingsClosed: { settingsClosed += 1 }) }
                    titleRow
                }
            }
            .sheet(item: $sharingMilestone) { m in
                ShareCardSheet(fileName: MoneyShareCard.fileName(kind: "milestone", date: m.achievedAt, calendar: .current)) { _, _ in
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

    /// The notification ask and the milestone card.
    @ViewBuilder
    private func asks(_ vehicle: Vehicle) -> some View {
        if !state.isDemo, state.notifications.shouldAskInApp {
            NotificationPromptCard(manager: state.notifications)
        }
        if let m = extras.current(vehicleID: vehicle.id) {
            MilestoneCard(milestone: m, failed: extras.dismissFailed, onShare: { sharingMilestone = m }, canHide: !state.isDemo) {
                Task { await extras.dismiss(m, source: state.client) }
            }
        }
    }

    /// The page title with the month stepper (and, with more than one truck, which one is shown), pinned under the top
    /// bar like the web's.
    private var titleRow: some View {
        // At accessibility sizes the truck and its state stack instead of squeezing side by side.
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(spacing: 8))
        return ThemeTitleRow(title: "Now") {
            layout {
                if vehicles.count > 1 {
                    Menu {
                        Picker("Vehicle", selection: Binding(get: { selectedID }, set: { id in
                            selectedID = id
                            pickedLocked = vehicles.first { $0.id == id }?.proLocked == true
                        })) {
                            ForEach(vehicles) { v in Text(VehicleChoice.label(v)).tag(v.id) }
                        }
                    } label: {
                        if v2 {
                            // The redesign drops the small car glyph (the web has none): the truck's name, with the chevron that says it opens.
                            HStack(spacing: 4) {
                                Text(vehicles.first { $0.id == selectedID }?.name ?? "Vehicle").themeText(.value, color: Theme.textSecondary).lineLimit(1)
                                Image(systemName: "chevron.up.chevron.down").font(.caption2).foregroundStyle(Theme.textSecondary)
                            }
                            .frame(minHeight: 44)
                        } else {
                            Image(systemName: "car.2").font(.body).foregroundStyle(Theme.textSecondary)
                                .frame(width: 36, height: 36)
                        }
                    }
                    .accessibilityLabel("Switch vehicle")
                }
                if v2, !tidy, let chip = chipText { ThemePill(text: chip, tone: chipTone, identifier: "now-chip") }
                // With the redesign the period stepper sits above the month's figures instead.
                if !v2 { PeriodControl() }
            }
        }
    }

    /// The truck's state beside the title, once there is something to say (never "Live": that is the hero's button).
    private var chipText: String? {
        guard vehicleState != nil || now != nil else { return nil }
        return NowChip.text(for: chipSummary, clock: .now)
    }

    /// The summary the chip speaks for: a parked truck takes the parked graph's own parked-since, so the chip, the graph's pill and its header say
    /// one parked time (the state run restarts at every wake-up).
    private var chipSummary: NowSummary {
        var s = freshness.shown(NowSummary(state: vehicleState, now: now, clock: .now), clock: .now)
        if parkedGraphOn, s.kind == .parked, let since = parkedSeries.series?.parkedSince { s.since = since }
        return s
    }

    private var chipTone: ThemePill.Tone {
        let s = chipSummary
        if s.kind == .charging, s.presence != .offline { return .live }
        return NowCard.pill(for: s, clock: .now).tone
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
        guard let id = selected?.id, selected?.proLocked != true else { return }
        async let c: Void = charts.load(source: state.client, vehicleID: id, month: month, force: force)
        async let p: Void = places.load(source: state.client, vehicleID: id, month: month, force: force)
        async let k: Void = kpi.load(source: state.client, vehicleID: id, month: month, scopeLedger: vehicles.count > 1,
                                      lastChargeEnd: lastChargeEnd, force: force)
        _ = await (c, p, k)
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
            selectedID = VehicleChoice.id(vehicles, chosen: selectedID, pickedLocked: pickedLocked) ?? ""
            await loadSelected()
            if force { await loadCharts(force: true) }
        } catch {
            loadError = UserFacingError.words(error)
        }
    }

    private func loadSelected() async {
        guard let id = selected?.id, selected?.proLocked != true else {
            vehicleState = nil
            stateVehicleID = nil
            now = nil
            session = nil
            liveDrive = nil
            liveDestination = nil
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
        vehicleState = NowReads.stateToShow(fresh: s, last: vehicleState, lastID: stateVehicleID, id: id)
        stateVehicleID = vehicleState == nil ? nil : id
        now = n ?? nil
        if v2, let t = now?.ambient?.tempC { coldRange = try? await client.coldRange(vehicleID: id, tempC: t.rounded(), grid: [], metric: NowFormat.usesMetric) } else { coldRange = nil }
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
        if vehicleState == nil && now == nil {
            loadError = "Couldn't load this truck's status."
        }
        if !state.isDemo { await syncLiveActivity() }
        await extras.load(source: client, vehicleID: id)
    }

    /// The session or drive reads that only make sense while charging or in gear.
    private func loadLive(id: String) async {
        // The demo truck has no live Rivian connection to read.
        if state.isDemo { session = nil; liveDrive = nil; liveDestination = nil; return }
        let kind = now?.state ?? NowSummary.inferState(from: vehicleState)
        session = kind == .charging ? (try? await state.client.liveSession(id: id)) ?? nil : nil
        if kind == .driving {
            // A failed read (503 while the owner pod is busy or rolling) keeps the card for a few polls; "no drive" (204) clears it.
            do { liveDrive = try await state.client.liveDrive(id: id); driveFailures = 0 } catch {
                driveFailures += 1
                if driveFailures >= 3 { liveDrive = nil }
            }
        } else {
            liveDrive = nil; driveFailures = 0
        }
        // Only while in gear, only when the design is on and not told "off"; this runs on the state's own cadence, which the tab and scene gate.
        // Only while the Live screen is open, or the Now hero shows it (ios_now_tidy): nothing else shows it.
        guard kind == .driving, showLive || tidy, state.designPreview.contains("live_destination"), !destinationOff else { liveDestination = nil; destinationFailures = 0; return }
        switch try? await state.client.liveDestination(id: id) {
        case .trip(let d): liveDestination = d; destinationFailures = 0
        case .noTrip: liveDestination = nil; destinationFailures = 0
        case .off: liveDestination = nil; destinationOff = true
        case nil:
            // A failed read (503): keep what is shown, but not for long; a stale trip is worse than none.
            destinationFailures += 1
            if destinationFailures >= 3 { liveDestination = nil }
        }
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
        var lastFull = Date.now
        while !Task.isCancelled {
            let kind = now?.state ?? NowSummary.inferState(from: vehicleState)
            let quick = NowRefresh.stateInterval(kind, demo: state.isDemo)
            try? await Task.sleep(for: .seconds(quick ?? NowRefresh.pollInterval(kind)))
            guard !Task.isCancelled, selected?.id != nil else { continue }
            // Driving: /state every 2 s for the speed, the rest (/now, the live reads) on the usual cadence.
            if quick != nil, Date.now.timeIntervalSince(lastFull) < NowRefresh.pollInterval(kind) {
                await refreshState()
            } else {
                await refreshNow()
                lastFull = .now
            }
        }
    }

    /// Only /state, from the server now: the speed while driving.
    private func refreshState() async {
        guard let id = selected?.id else { return }
        let conn = state.connection
        let client = state.client
        guard let s = await NowReads.logged("/state", record: { conn.record(.state, $0) }, { try await client.vehicleState(id: id) }) else { return }
        vehicleState = s
        freshness.fetched(at: .now)
        publishConnection()
        clockTick = .now
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
