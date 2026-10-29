import SwiftUI

/// The Plan tab: where to, the charge to start with, when to leave, and the plan Rivian's planner gives back (the stops, how long, an
/// estimate of what the fast charging costs). It appears only where the server has the trip planner. The truck's position and charge
/// come from the truck; the phone's own location is never read.
struct TripPlanView: View {
    @Environment(AppState.self) private var state
    @State private var model = TripPlanModel()
    @State private var saved = SavedTripsModel()
    @State private var demoTrips = DemoTripsModel()
    @State private var picking: Picking?

    enum Picking: Identifiable {
        case origin, destination
        var id: Int { self == .origin ? 0 : 1 }
    }

    var body: some View {
        NavigationStack {
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                        if state.isDemo {
                            // The demo plans nothing and saves nothing: it shows the sample trips, as the web's /sample-trips does.
                            DemoTripsList(model: demoTrips, plan: model)
                            TripPlanResult(model: model, metric: NowFormat.usesMetric)
                        } else {
                            TripPlanForm(model: model, onPick: { picking = $0 }, onPlan: { Task { await model.plan(source: state.client) } })
                            TripPlanResult(model: model, metric: NowFormat.usesMetric)
                            if model.plan != nil { SaveTripSection(model: model, saved: saved) }
                            if let active = saved.active { PlanVsActualSection(saved: active) }
                            SavedTripsList(model: model, saved: saved)
                        }
                    }
                    .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 4 * Theme.unit)
                }
                .onChange(of: model.focusedStop) { _, i in
                    guard let i else { return }
                    withAnimation { proxy.scrollTo("stop-\(i)", anchor: .center) }
                }
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .toolbar(.hidden, for: .navigationBar)
            .safeAreaInset(edge: .top, spacing: 0) {
                VStack(spacing: 0) {
                    ThemeTopBar { AccountMenu(vehicleID: model.vehicleID) }
                    ThemeTitleRow(title: "Plan") {}
                }
            }
            .sheet(item: $picking) { which in
                PlaceSearchSheet(model: model, title: which == .origin ? "Start from" : "Where to") { place in
                    if which == .origin { model.origin = place } else { model.destination = place }
                }
                .followsAppearance()
            }
            .task {
                if state.isDemo { await demoTrips.load(source: state.client); return }
                let vehicles = (try? await state.client.vehicles()) ?? []
                await model.loadTruck(source: state.client, vehicles: vehicles)
                await saved.load(source: state.client)
            }
        }
    }
}

struct TripPlanForm: View {
    @Bindable var model: TripPlanModel
    let onPick: (TripPlanView.Picking) -> Void
    let onPlan: () -> Void

    var body: some View {
        ThemeSection(title: "Plan a trip") {
            placeRow(label: "From", value: model.origin?.line ?? "Where the truck is", action: { onPick(.origin) }, clear: model.origin.map { _ in { model.origin = nil } })
            Divider().overlay(Theme.border)
            placeRow(label: "To", value: model.destination?.line ?? "Choose a place", action: { onPick(.destination) }, clear: nil, placeholder: model.destination == nil)
            Divider().overlay(Theme.border)
            Toggle(isOn: $model.useTruckCharge) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Start with the truck's charge").themeText(.value, color: Theme.textPrimary)
                    if let live = model.liveSoc { Text("\(Int(live.rounded()))% now").themeText(.subline, color: Theme.textSecondary) }
                }
            }
            .tint(Theme.accent).frame(minHeight: 44)
            if !model.useTruckCharge {
                Stepper(value: $model.startingSoc, in: 10...100, step: 5) {
                    Text("Start with \(Int(model.startingSoc))%").themeText(.value, color: Theme.textPrimary)
                }
                .frame(minHeight: 44)
            }
            Stepper(value: $model.arrivalTarget, in: TripPlanLogic.arrivalTargetRange, step: TripPlanLogic.arrivalTargetStep) {
                Text("Arrival battery \(Int(model.arrivalTarget))%").themeText(.value, color: Theme.textPrimary)
            }
            .frame(minHeight: 44)
            .accessibilityHint("The charge to arrive with; the planner adds charging stops to keep it")
            Toggle(isOn: $model.leaveNow) { Text("Leave now").themeText(.value, color: Theme.textPrimary) }
                .tint(Theme.accent).frame(minHeight: 44)
            if !model.leaveNow {
                DatePicker("Leave at", selection: $model.departAt, in: Date()..., displayedComponents: [.date, .hourAndMinute])
                    .themeText(.value, color: Theme.textPrimary).frame(minHeight: 44)
            }
            Button(action: onPlan) {
                Text(model.state == .planning ? "Planning…" : "Plan the trip").frame(maxWidth: .infinity)
            }
            .buttonStyle(ThemeButtonStyle())
            .disabled(!model.canPlan)
            .accessibilityHint("Asks Rivian's planner for the route and the charging stops")
        }
    }

    private func placeRow(label: String, value: String, action: @escaping () -> Void, clear: (() -> Void)?, placeholder: Bool = false) -> some View {
        HStack(spacing: 3 * Theme.unit) {
            Text(label).themeText(.label).frame(width: 44, alignment: .leading)
            Button(action: action) {
                Text(value).themeText(.value, color: placeholder ? Theme.textTertiary : Theme.textPrimary)
                    .multilineTextAlignment(.leading).frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
            }
            .buttonStyle(.plain)
            if let clear {
                Button("Use the truck", action: clear).font(Theme.sans(ThemeTokens.typeSubline)).tint(Theme.accent)
            }
        }
    }
}

struct TripPlanResult: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel
    let metric: Bool

    var body: some View {
        switch model.state {
        case .idle:
            EmptyView()
        case .planning:
            HStack(spacing: 8) { ProgressView().tint(Theme.textSecondary); Text("Asking the planner…").themeText(.value, color: Theme.textSecondary) }
        case .failed(let words):
            Text(words).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
        case .planned(let plan):
            if let route = plan.best { content(route, plan: plan) }
        }
    }

    @ViewBuilder
    private func content(_ route: TripRoute, plan: TripPlan) -> some View {
        if case .planned = model.night {
            LongDriveSection(model: model, metric: metric)
        } else {
            PlanRouteMap(route: route, onStop: { model.focusedStop = $0 }, focus: model.focusedStop, along: model.chargers, onLine: { model.roadLine = $0 })
            if !state.isDemo { AlongToggle(model: model) }
            ThemeSection(title: "The plan") {
                if let arrive = TripPlanLogic.arrival(route, timeZone: TripPlanLogic.zone(plan.destinationTz) ?? .current) {
                    Text("Arrive \(arrive)").themeText(.title, color: Theme.textPrimary)
                }
                Text(TripPlanLogic.summary(route, metric: metric)).themeText(.value, color: Theme.textBody)
                if let soc = route.arrivalSoC {
                    let low = TripPlanLogic.lowArrival(soc, target: model.plannedTarget)
                    Text("Arrive with about \(Int(soc.rounded()))%").themeText(.subline, color: low == nil ? Theme.textSecondary : Theme.estimate)
                    if let low { Text(low).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true) }
                }
                if let warning = TripPlanLogic.shortfall(route, metric: metric) {
                    Text(warning).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                }
            }
            if TripPlanLogic.isLongDrive(route), !state.isDemo { LongDriveSection(model: model, metric: metric) }
            ThemeSection(title: TripPlanLogic.stopsTitle(route)) {
                StopRows(route: route, cost: plan.costs?.first, base: 0, focused: model.focusedStop, metric: metric) { model.focusedStop = $0 }
                if route.chargingStops.isEmpty { Text(TripPlanLogic.noStopsLine(route)).themeText(.value, color: Theme.textSecondary) }
            }
            if let cost = TripPlanLogic.costLine(plan.costs?.first) {
                ThemeSection(title: "Cost") {
                    Text(cost).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                    if let home = TripPlanLogic.homeLine(plan.costs?.first) { Text(home).themeText(.subline, color: Theme.textSecondary) }
                }
            }
        }
    }
}

/// Type a place, pick a match. The text goes to the server's own geocoder in the request's query; nothing is kept.
struct PlaceSearchSheet: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss
    let model: TripPlanModel
    let title: String
    let onPick: (PlaceMatch) -> Void
    @State private var query = ""
    @FocusState private var focused: Bool

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 3 * Theme.unit) {
                    TextField("A city, or a street and a city", text: $query)
                        .submitLabel(.search).focused($focused).autocorrectionDisabled()
                        .onSubmit { Task { await model.search(source: state.client, query: query) } }
                        .task(id: query) { await model.typeAhead(source: state.client, query: query) }
                        .overlay(alignment: .trailing) {
                            if model.searching && query.trimmingCharacters(in: .whitespaces).count >= TripPlanLogic.typeAheadMinChars {
                                ProgressView().tint(Theme.textSecondary).padding(.trailing, 3 * Theme.unit)
                            }
                        }
                        .padding(.horizontal, 3 * Theme.unit).frame(minHeight: 44)
                        .background(Theme.background, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
                    if let m = model.searchMessage { Text(m).themeText(.subline, color: Theme.textSecondary) }
                    ForEach(model.results) { place in
                        Button {
                            onPick(place)
                            model.clearSearch()
                            dismiss()
                        } label: {
                            Text(place.line).themeText(.value, color: Theme.textPrimary).multilineTextAlignment(.leading)
                                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                        }
                        .buttonStyle(.plain)
                        Divider().overlay(Theme.border)
                    }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { model.clearSearch(); dismiss() }.tint(Theme.accent) } }
        }
        .presentationDetents([.large])
    }
}

/// A drive of more than eight hours: offer the same trip over two days, with a night at a stay the person picks.
struct LongDriveSection: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel
    let metric: Bool

    var body: some View {
        switch model.night {
        case .none:
            ThemeSection(title: "Long drive") {
                Text("Plan it over two days?").themeText(.value, color: Theme.textPrimary)
                Button("Find a place to sleep") { Task { await model.findNight(source: state.client) } }
                    .buttonStyle(ThemeSecondaryButtonStyle())
            }
        case .looking:
            ThemeSection(title: "Long drive") {
                HStack(spacing: 8) { ProgressView().tint(Theme.textSecondary); Text("Looking for places to sleep…").themeText(.value, color: Theme.textSecondary) }
            }
        case .choose(let stays):
            ThemeSection(title: "Where to sleep") {
                ForEach(Array(stays.enumerated()), id: \.element.id) { i, stay in
                    if i > 0 { Divider().overlay(Theme.border) }
                    Button { Task { await model.planNight(source: state.client, at: stay) } } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(stay.name).themeText(.value, color: Theme.textPrimary).multilineTextAlignment(.leading)
                            Text(TripPlanLogic.candidateLine(stay, metric: metric)).themeText(.subline, color: Theme.textSecondary)
                        }
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    }
                    .buttonStyle(.plain)
                }
                Text("Listings only. Rivolt can't tell whether a charger is free or working.").themeText(.subline, color: Theme.textSecondary)
            }
        case .chooseFallback(let found):
            ThemeSection(title: "Where to sleep") {
                Text("No hotel with a listed charger is in range. These hotels list no charger: you would sleep without charging and start the next day at a fast charger.")
                    .themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                ForEach(Array(found.fallbackStays.enumerated()), id: \.element.id) { i, stay in
                    Divider().overlay(Theme.border)
                    Button { Task { await model.planNight(source: state.client, at: TripPlanLogic.candidate(stay), morning: stay.fastCharger) } } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(stay.name).themeText(.value, color: Theme.textPrimary).multilineTextAlignment(.leading)
                            Text(TripPlanLogic.fallbackLine(stay, metric: metric)).themeText(.subline, color: Theme.estimate)
                                .multilineTextAlignment(.leading).fixedSize(horizontal: false, vertical: true)
                        }
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    }
                    .buttonStyle(.plain)
                }
                OpenStreetMapCredit(found: found)
            }
        case .planning:
            ThemeSection(title: "Two days") {
                HStack(spacing: 8) { ProgressView().tint(Theme.textSecondary); Text("Asking the planner…").themeText(.value, color: Theme.textSecondary) }
            }
        case .failed(let words):
            ThemeSection(title: "Long drive") {
                Text(words).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                Button("Try again") { Task { await model.findNight(source: state.client) } }.buttonStyle(ThemeSecondaryButtonStyle())
            }
        case .planned(let trip):
            MultidaySections(trip: trip, metric: metric, model: model)
            if !state.isDemo { Button("Back to one day") { model.clearNight() }.buttonStyle(ThemeSecondaryButtonStyle()) }
        }
    }
}

struct MultidaySections: View {
    let trip: TripMultiday
    let metric: Bool
    let model: TripPlanModel

    var body: some View {
        PlanDaysMap(days: trip.days, focus: model.focusedStop, onStop: { model.focusedStop = $0 })
        let offsets = PlanRouteLogic.pinOffsets(trip.days)
        ForEach(Array(trip.days.enumerated()), id: \.element.id) { n, day in
            ThemeSection(title: "Day \(day.index)") {
                let previous = trip.days.first { $0.index == day.index - 1 }
                let line = TripPlanLogic.dayLine(
                    day, since: trip.days.first?.departAt?.date,
                    departZone: TripPlanLogic.zone(previous?.endTz) ?? .current, arriveZone: TripPlanLogic.zone(day.endTz) ?? .current)
                if !line.isEmpty { Text(line).themeText(.title, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true) }
                if let route = day.plan.best {
                    Text(TripPlanLogic.summary(route, metric: metric)).themeText(.value, color: Theme.textBody)
                    Text(TripPlanLogic.stopsTitle(route)).themeText(.subline, color: Theme.textSecondary)
                    if route.chargingStops.isEmpty {
                        Text(TripPlanLogic.noStopsLine(route)).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                    } else {
                        Divider().overlay(Theme.border)
                        StopRows(route: route, cost: day.costs?.first, base: offsets[n], focused: model.focusedStop, metric: metric, finish: "where the day ends") { model.focusedStop = $0 }
                    }
                    if let cost = TripPlanLogic.costLine(day.costs?.first) {
                        Text(cost).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                    }
                }
                if let low = TripPlanLogic.lowArrival(day.arrivalSoc, target: model.plannedTarget) {
                    Text(low).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                }
                if let o = day.overnight {
                    Divider().overlay(Theme.border)
                    let nightIndex = offsets[n] + PlanRouteLogic.dayPins(day).stops.count
                    Button { model.focusedStop = nightIndex } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Night at \(o.name ?? "the stay")").themeText(.value, color: model.focusedStop == nightIndex ? Theme.accent : Theme.textPrimary)
                            Text(TripPlanLogic.nightLine(o)).themeText(.subline, color: Theme.textSecondary)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .buttonStyle(.plain)
                    .id("stop-\(nightIndex)")
                }
            }
        }
        if trip.failed {
            Text("Day \(trip.failedDay ?? 0) couldn't be planned, so the days after it are missing. Try another place to sleep.")
                .themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
        }
    }
}

/// Save the trip on screen under a name, or over the saved trip it was opened from.
struct SaveTripSection: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel
    let saved: SavedTripsModel
    @State private var name = ""

    var body: some View {
        ThemeSection(title: "Save this trip") {
            TextField("Name", text: $name)
                .submitLabel(.done).autocorrectionDisabled()
                .padding(.horizontal, 3 * Theme.unit).frame(minHeight: 44)
                .background(Theme.background, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
            Button(saved.saveState == .saving ? "Saving…" : "Save as a new trip") { Task { await save(overwrite: false) } }
                .buttonStyle(ThemeSecondaryButtonStyle())
                .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty || saved.saveState == .saving)
            if let active = saved.active {
                Button("Update “\(active.name)”") { Task { await save(overwrite: true) } }
                    .buttonStyle(ThemeSecondaryButtonStyle())
                    .disabled(saved.saveState == .saving)
            }
            switch saved.saveState {
            case .saved(let n): Text("Saved as “\(n)”.").themeText(.subline, color: Theme.textSecondary)
            case .failed(let words): Text(words).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
            default: EmptyView()
            }
        }
        .onAppear { if name.isEmpty { name = saved.active?.name ?? model.suggestedName ?? "" } }
        .onChange(of: model.destination) { _, _ in if saved.active == nil { name = model.suggestedName ?? "" } }
    }

    private func save(overwrite: Bool) async {
        guard let inputs = model.savedInputs() else { return }
        await saved.save(source: state.client, name: overwrite ? (saved.active?.name ?? name) : name, inputs: inputs, plan: model.savedPlan(), overwrite: overwrite)
    }
}

/// The saved trips, newest first as the server lists them. A tap shows one as it was saved, with no planner call.
struct SavedTripsList: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel
    let saved: SavedTripsModel
    @State private var message: String?

    var body: some View {
        Group { content }
            // A tapped trip-recap notification arrives as a pending route; the list may not have loaded yet.
            .task(id: state.notifications.pendingRoute) { await openNotifiedTrip() }
    }

    @ViewBuilder
    private var content: some View {
        if !saved.trips.isEmpty || saved.phase != .loaded {
            ThemeSection(title: "Saved trips") {
                if saved.trips.isEmpty, case .failed(let words) = saved.phase {
                    Text(words).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                } else if saved.trips.isEmpty {
                    HStack(spacing: 8) { ProgressView().tint(Theme.textSecondary); Text("Loading…").themeText(.value, color: Theme.textSecondary) }
                } else {
                    ForEach(Array(saved.trips.enumerated()), id: \.element.id) { i, trip in
                        if i > 0 { Divider().overlay(Theme.border) }
                        Button { open(trip) } label: {
                            Text(trip.name).themeText(.value, color: saved.active?.id == trip.id ? Theme.accent : Theme.textPrimary)
                                .multilineTextAlignment(.leading)
                                .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                        }
                        .buttonStyle(.plain)
                    }
                }
                if let message { Text(message).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true) }
            }
        }
    }

    /// A tapped trip-recap notification: open that trip, so its Plan vs actual is on screen. The route is cleared either way.
    private func openNotifiedTrip() async {
        guard case .trip(let id)? = state.notifications.pendingRoute else { return }
        defer { state.notifications.pendingRoute = nil }
        if saved.trips.isEmpty { await saved.load(source: state.client) }
        if let trip = saved.trips.first(where: { $0.id == id }) { open(trip) }
    }

    private func open(_ trip: SavedTrip) {
        guard let r = SavedTripLogic.restore(trip) else {
            message = "“\(trip.name)” can't be opened here. Open it on the web."
            return
        }
        message = nil
        model.open(r)
        saved.opened(trip)
    }
}

/// How the saved trip on screen compared with the outing that was driven: a suggestion to confirm, or the figures once a drive is
/// linked. Figures only where the server gave one; an unfinished trip isn't compared.
struct PlanVsActualSection: View {
    @Environment(AppState.self) private var state
    let saved: SavedTrip
    @State private var model = PlanVsActualModel()

    var body: some View {
        ThemeSection(title: "Plan vs actual") {
            switch model.phase {
            case .idle, .loading:
                HStack(spacing: 8) { ProgressView().tint(Theme.textSecondary); Text("Looking…").themeText(.value, color: Theme.textSecondary) }
            case .failed(let words):
                Text(words).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                Button("Try again") { Task { await model.load(source: state.client, savedID: saved.id) } }.buttonStyle(ThemeSecondaryButtonStyle())
            case .loaded(let parts):
                ForEach(Array(parts.enumerated()), id: \.offset) { i, part in
                    if i > 0 { Divider().overlay(Theme.border) }
                    if parts.count > 1 { Text("Day \(part.day ?? i + 1)").themeText(.title, color: Theme.textPrimary) }
                    PlanVsActualPart(part: part, busy: model.busy) { tripID, link in
                        Task {
                            if link { await model.link(source: state.client, savedID: saved.id, tripID: tripID, day: parts.count > 1 ? part.day : nil) }
                            else { await model.unlink(source: state.client, savedID: saved.id, tripID: tripID, day: parts.count > 1 ? part.day : nil) }
                        }
                    }
                }
            }
            if let m = model.message { Text(m).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true) }
        }
        .task(id: saved.id) { await model.load(source: state.client, savedID: saved.id) }
    }
}

struct PlanVsActualPart: View {
    let part: PlanVsActual
    let busy: Bool
    /// (the outing's id, true to link or false to unlink)
    let act: (String, Bool) -> Void

    var body: some View {
        let top = part.candidates?.first
        switch PlanVsActualLogic.state(part) {
        case .none:
            Text("We'll compare once you've driven it.").themeText(.value, color: Theme.textSecondary)
        case .linkLost:
            Text("We can't find the trip you linked any more.").themeText(.value, color: Theme.textSecondary)
            if let id = part.link?.tripId { Button("Unlink") { act(id, false) }.buttonStyle(ThemeSecondaryButtonStyle()).disabled(busy) }
        case .inProgress:
            Text("Trip in progress. We'll compare when you're home.").themeText(.value, color: Theme.textSecondary)
        case .suggested:
            Text(PlanVsActualLogic.question(top)).themeText(.value, color: Theme.textPrimary)
            if let c = part.comparison { Figures(comparison: c, candidateMiles: top?.miles) }
            if let id = top?.tripId {
                Button("Confirm") { act(id, true) }.buttonStyle(ThemeSecondaryButtonStyle()).disabled(busy)
                Button("Not this trip") { act(id, false) }.buttonStyle(ThemeSecondaryButtonStyle()).disabled(busy)
            }
        case .linkedFull, .linkedPartial:
            if let c = part.comparison { Figures(comparison: c, candidateMiles: nil) }
            if part.link?.linkStale == true {
                Text("This match was re-found after your data changed; check it still looks right.").themeText(.subline, color: Theme.textSecondary)
            }
            if let id = part.link?.tripId { Button("Not this trip? Unlink") { act(id, false) }.buttonStyle(ThemeSecondaryButtonStyle()).disabled(busy) }
        }
    }
}

private struct Figures: View {
    let comparison: PlanVsActual.Comparison
    let candidateMiles: Double?

    var body: some View {
        if let header = PlanVsActualLogic.costHeader(comparison) { Text(header).themeText(.value, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true) }
        Text(PlanVsActualLogic.driven(comparison, candidateMiles: candidateMiles)).themeText(.subline, color: Theme.textSecondary)
        ForEach(Array(PlanVsActualLogic.rows(comparison).enumerated()), id: \.offset) { _, row in
            Divider().overlay(Theme.border)
            VStack(alignment: .leading, spacing: 2) {
                Text(row.label).themeText(.value, color: Theme.textPrimary)
                if let incomplete = row.incomplete {
                    Text("Not final yet. \(incomplete).").themeText(.subline, color: Theme.estimate)
                }
                let line = [row.planned.map { "Planned \($0)" }, row.actual.map { "actual \($0)\(row.estimated ? " (estimated)" : "")" }, row.delta]
                    .compactMap { $0 }.joined(separator: " · ")
                if !line.isEmpty { Text(line).themeText(.subline, color: row.watch ? Theme.estimate : Theme.textSecondary) }
                ForEach(row.reasons, id: \.self) { Text($0).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true) }
            }
            .accessibilityElement(children: .combine)
        }
    }
}

/// The credit these hotels need wherever they show: "© OpenStreetMap contributors", with the ODbL licence it is under as a link.
struct OpenStreetMapCredit: View {
    let found: OvernightCandidates

    var body: some View {
        HStack(spacing: 4) {
            Text(found.attribution ?? TripPlanLogic.osmCredit).themeText(.subline, color: Theme.textSecondary)
            if let url = URL(string: found.attributionLicenseUrl ?? TripPlanLogic.odblURL), url.scheme == "https" {
                Link("ODbL licence", destination: url).font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).tint(Theme.accent)
            }
        }
        .accessibilityElement(children: .combine)
    }
}

/// A route's charging stops as rows: the name, the charge it arrives with and leaves with, the minutes, the leg that led to it and
/// what it is expected to cost (an estimate, or "price shown at the charger"). A tap rings that stop on the map; a tap on a pin
/// scrolls here. `base` is where this route's pins start in the map's numbering.
struct StopRows: View {
    let route: TripRoute
    let cost: TripCost?
    let base: Int
    let focused: Int?
    let metric: Bool
    /// Where the last leg ends: "the destination" for a one-day plan, "where the day ends" for a day of a longer trip.
    var finish = "the destination"
    let onPick: (Int) -> Void

    var body: some View {
        let legs = TripPlanLogic.legs(route)
        let pins = pinIndexes
        ForEach(Array(route.chargingStops.enumerated()), id: \.element.id) { i, stop in
            if i > 0 { Divider().overlay(Theme.border) }
            let pin = pins[i].map { base + $0 }
            Button { if let pin { onPick(pin) } } label: {
                VStack(alignment: .leading, spacing: 2) {
                    Text(stop.name ?? "Charger").themeText(.value, color: pin != nil && focused == pin ? Theme.accent : Theme.textPrimary)
                        .fixedSize(horizontal: false, vertical: true)
                    let detail = TripPlanLogic.stopDetail(stop)
                    if !detail.isEmpty { Text(detail).themeText(.subline, color: Theme.textSecondary) }
                    if let leg = TripPlanLogic.legLine(i < legs.stops.count ? legs.stops[i] : nil, first: i == 0, metric: metric) {
                        Text(leg).themeText(.subline, color: Theme.textSecondary)
                    }
                    if let price = TripPlanLogic.stopCostLine(cost, index: i) { Text(price).themeText(.subline, color: Theme.textSecondary) }
                    if stop.adapterRequired == true { Text("Needs a Tesla adapter").themeText(.subline, color: Theme.estimate) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.vertical, Theme.unit)
            }
            .buttonStyle(.plain)
            .accessibilityElement(children: .combine)
            .id(pin.map { "stop-\($0)" } ?? "stop-x\(base)-\(i)")
        }
        if let last = legs.last, last > 0 {
            Text("Last leg: \(TripPlanLogic.distance(meters: last, metric: metric)) to \(finish)").themeText(.subline, color: Theme.textSecondary)
        }
    }

    /// Each stop's pin on the map (stops with no place have none), counted as the map counts them.
    private var pinIndexes: [Int?] {
        var n = 0
        return route.chargingStops.map { s in
            guard s.latitude != nil, s.longitude != nil else { return nil }
            defer { n += 1 }
            return n
        }
    }
}

/// "Show fast chargers along the route": the DC fast chargers of 100 kW or more near it, drawn small on the map. A tap on one says
/// what it is. Hidden when the server has no charger listing.
struct AlongToggle: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel

    var body: some View {
        if model.along != .unavailable {
            VStack(alignment: .leading, spacing: 2) {
                Toggle("Show fast chargers along the route", isOn: Binding(
                    get: { model.along != .off },
                    set: { on in Task { await model.toggleAlong(on: on, source: state.client) } }))
                    .tint(Theme.accent).frame(minHeight: 44)
                    .disabled(model.along == .loading)
                switch model.along {
                case .loading: Text("Looking…").themeText(.subline, color: Theme.textSecondary)
                case .shown(let c): Text(TripPlanLogic.alongCount(c.count)).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                case .failed(let words): Text(words).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                default: EmptyView()
                }
                if let picked = model.pickedCharger { Text(TripPlanLogic.alongLine(picked)).themeText(.value, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true) }
            }
        }
    }
}

/// The demo's sample trips: tap one to open it, read only. Its plan is the one stored on the server; nothing is planned or saved.
struct DemoTripsList: View {
    @Environment(AppState.self) private var state
    let model: DemoTripsModel
    let plan: TripPlanModel

    var body: some View {
        ThemeSection(title: "Sample trips") {
            Text("Examples to open. In the demo nothing is planned or saved.").themeText(.subline, color: Theme.textSecondary)
            switch model.phase {
            case .loading:
                HStack(spacing: 8) { ProgressView().tint(Theme.textSecondary); Text("Loading…").themeText(.value, color: Theme.textSecondary) }
            case .failed(let words):
                Text(words).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                Button("Try again") { Task { await model.load(source: state.client) } }.buttonStyle(ThemeSecondaryButtonStyle())
            case .loaded(let presets):
                ForEach(Array(presets.enumerated()), id: \.element.id) { _, preset in
                    Divider().overlay(Theme.border)
                    Button { Task { await model.open(preset, source: state.client, on: plan) } } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(preset.name).themeText(.value, color: model.openedID == preset.id ? Theme.accent : Theme.textPrimary)
                                .multilineTextAlignment(.leading)
                            Text(DemoTripLogic.summary(preset.summary, metric: NowFormat.usesMetric)).themeText(.subline, color: Theme.textSecondary)
                                .multilineTextAlignment(.leading).fixedSize(horizontal: false, vertical: true)
                        }
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    }
                    .buttonStyle(.plain)
                    .disabled(model.opening != nil)
                    .overlay(alignment: .trailing) { if model.opening == preset.id { ProgressView().tint(Theme.textSecondary) } }
                }
            }
            if let m = model.message { Text(m).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true) }
        }
    }
}
