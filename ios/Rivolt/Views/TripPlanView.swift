import SwiftUI

/// The Plan tab: where to, the charge to start with, when to leave, and the plan Rivian's planner gives back (the stops, how long, an
/// estimate of what the fast charging costs). It appears only where the server has the trip planner. The truck's position and charge
/// come from the truck; the phone's own location is never read.
struct TripPlanView: View {
    @Environment(AppState.self) private var state
    @State private var model = TripPlanModel()
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
                        TripPlanForm(model: model, onPick: { picking = $0 }, onPlan: { Task { await model.plan(source: state.client) } })
                        TripPlanResult(model: model, metric: NowFormat.usesMetric)
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
            }
            .task {
                let vehicles = (try? await state.client.vehicles()) ?? []
                await model.loadTruck(source: state.client, vehicles: vehicles)
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
            PlanRouteMap(route: route) { model.focusedStop = $0 }
            ThemeSection(title: "The plan") {
                if let arrive = TripPlanLogic.arrival(route, timeZone: TripPlanLogic.zone(plan.destinationTz) ?? .current) {
                    Text("Arrive \(arrive)").themeText(.title, color: Theme.textPrimary)
                }
                Text(TripPlanLogic.summary(route, metric: metric)).themeText(.value, color: Theme.textBody)
                if let soc = route.arrivalSoC {
                    let low = TripPlanLogic.lowArrival(soc)
                    Text("Arrive with about \(Int(soc.rounded()))%").themeText(.subline, color: low == nil ? Theme.textSecondary : Theme.estimate)
                    if let low { Text(low).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true) }
                }
                if let warning = TripPlanLogic.shortfall(route, metric: metric) {
                    Text(warning).themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                }
            }
            if TripPlanLogic.isLongDrive(route) { LongDriveSection(model: model, metric: metric) }
            ThemeSection(title: TripPlanLogic.stopsTitle(route)) {
                ForEach(Array(route.chargingStops.enumerated()), id: \.element.id) { i, stop in
                    if i > 0 { Divider().overlay(Theme.border) }
                    VStack(alignment: .leading, spacing: 2) {
                        Text(stop.name ?? "Charger").themeText(.value, color: model.focusedStop == i ? Theme.accent : Theme.textPrimary).fixedSize(horizontal: false, vertical: true)
                        let detail = TripPlanLogic.stopDetail(stop)
                        if !detail.isEmpty { Text(detail).themeText(.subline, color: Theme.textSecondary) }
                        if stop.adapterRequired == true { Text("Needs a Tesla adapter").themeText(.subline, color: Theme.estimate) }
                    }
                    .padding(.vertical, Theme.unit)
                    .accessibilityElement(children: .combine)
                    .id("stop-\(i)")
                }
                if route.chargingStops.isEmpty { Text("The battery reaches the destination without charging.").themeText(.value, color: Theme.textSecondary) }
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
                        .padding(.horizontal, 3 * Theme.unit).frame(minHeight: 44)
                        .background(Theme.background, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
                    Button(model.searching ? "Searching…" : "Search") { focused = false; Task { await model.search(source: state.client, query: query) } }
                        .buttonStyle(ThemeSecondaryButtonStyle())
                        .disabled(query.trimmingCharacters(in: .whitespaces).count < 3 || model.searching)
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
            MultidaySections(trip: trip, metric: metric)
            Button("Back to one day") { model.clearNight() }.buttonStyle(ThemeSecondaryButtonStyle())
        }
    }
}

struct MultidaySections: View {
    let trip: TripMultiday
    let metric: Bool

    var body: some View {
        ForEach(trip.days) { day in
            ThemeSection(title: "Day \(day.index)") {
                let previous = trip.days.first { $0.index == day.index - 1 }
                let line = TripPlanLogic.dayLine(
                    day, since: trip.days.first?.departAt?.date,
                    departZone: TripPlanLogic.zone(previous?.endTz) ?? .current, arriveZone: TripPlanLogic.zone(day.endTz) ?? .current)
                if !line.isEmpty { Text(line).themeText(.title, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true) }
                if let route = day.plan.best {
                    Text(TripPlanLogic.summary(route, metric: metric)).themeText(.value, color: Theme.textBody)
                    Text(TripPlanLogic.stopsTitle(route)).themeText(.subline, color: Theme.textSecondary)
                }
                if let low = TripPlanLogic.lowArrival(day.arrivalSoc) {
                    Text(low).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                }
                if let o = day.overnight {
                    Divider().overlay(Theme.border)
                    Text("Night at \(o.name ?? "the stay")").themeText(.value, color: Theme.textPrimary)
                    Text(TripPlanLogic.nightLine(o)).themeText(.subline, color: Theme.textSecondary)
                }
            }
        }
        if trip.failed {
            Text("Day \(trip.failedDay ?? 0) couldn't be planned, so the days after it are missing. Try another place to sleep.")
                .themeText(.value, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
        }
    }
}
