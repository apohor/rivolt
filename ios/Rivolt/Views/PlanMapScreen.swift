import SwiftUI

/// A signed-in plan, map first: the route on a full-height map that can be panned and zoomed, and the itinerary in a sheet with three resting
/// heights over its bottom. A pin and its row in the sheet are one selection: pick either and the other follows (the map eases to a stop that is
/// out of view, the sheet opens to its row). A two-day trip has day chips (and the "Driving per day" row) in the sheet.
struct PlanMapScreen: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel
    let saved: SavedTripsModel
    let onEdit: () -> Void

    @State private var detent = PlanSheetDetent.half
    @State private var day: Int?

    private var metric: Bool { NowFormat.usesMetric }

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .bottom) {
                map(coverHeight: DetentSheet<EmptyView, EmptyView>.height(PlanSheetDetent.half, in: geo.size.height))
                    .overlay(alignment: .topLeading) { overlays }
                if let c = model.pickedCharger {
                    PlanPinCallout(charger: c, offRoute: TripPlanLogic.offRouteLine(TripPlanLogic.offRouteMeters(lat: c.lat, lon: c.lon, line: model.roadLine), metric: metric),
                                   adding: model.updating, onAdd: { Task { await model.addVia(c, source: state.client) } }, onClose: { model.closeCallout() })
                        .padding(.horizontal, 12)
                        .padding(.bottom, DetentSheet<EmptyView, EmptyView>.height(detent, in: geo.size.height) + 10)
                        .frame(maxHeight: .infinity, alignment: .bottom)
                }
                DetentSheet(detent: $detent, container: geo.size.height) {
                    header
                } content: {
                    ScrollViewReader { proxy in
                        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                            TripPlanResult(model: model, metric: metric, showsMap: false, onlyDay: day)
                            if model.plan != nil { SaveTripSection(model: model, saved: saved) }
                        }
                        .onChange(of: model.focusedStop) { _, i in
                            guard let i else { return }
                            if detent == .peek { detent = .half }
                            withAnimation { proxy.scrollTo("stop-\(i)", anchor: .center) }
                        }
                    }
                }
            }
        }
        .ignoresSafeArea(.container, edges: .bottom)
    }

    /// The layer chip over the map, and the state of a re-plan.
    private var overlays: some View {
        VStack(alignment: .leading, spacing: 8) {
            PlanLayerChips(model: model)
            if model.updating {
                HStack(spacing: 8) { ProgressView().tint(Theme.textSecondary); Text("Updating route…").themeText(.subline, color: Theme.textPrimary) }
                    .padding(.horizontal, 12).frame(minHeight: 36)
                    .background(Theme.card.opacity(0.92), in: Capsule())
                    .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
                    .accessibilityElement(children: .combine)
            }
            if let problem = model.updateProblem {
                Text(problem).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
                    .padding(10).background(Theme.card.opacity(0.95), in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
            }
        }
        .padding(12)
    }

    @ViewBuilder
    private func map(coverHeight: CGFloat) -> some View {
        if case .planned(let trip) = model.night {
            PlanDaysMap(days: trip.days, focus: model.focusedStop, onStop: { model.focusedStop = $0 }, fills: true, bottomInset: coverHeight, day: day)
        } else if let route = model.plan?.best {
            PlanRouteMap(route: route, onStop: { model.focusedStop = $0 }, focus: model.focusedStop, along: model.markers,
                         onLine: { model.roadLine = $0 }, fills: true, bottomInset: coverHeight,
                         onLongPress: { model.dropPin(lat: $0.lat, lon: $0.lon) }, vias: model.vias, pendingVias: model.pendingVias)
        }
    }

    @ViewBuilder
    private var header: some View {
        PlanSheetHeader(title: PlanSheetLogic.title(origin: model.origin?.line, destination: model.destination?.line),
                        summary: model.plan?.best.map { TripPlanLogic.summary($0, metric: metric) }, onEdit: onEdit) {
            if case .planned(let trip) = model.night, trip.days.count > 1 { dayChips(trip) }
        }
    }

    private func dayChips(_ trip: TripMultiday) -> some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                chip("Whole trip", on: day == nil) { day = nil }
                ForEach(trip.days) { d in chip("Day \(d.index)", on: day == d.index) { day = d.index } }
            }
        }
    }

    private func chip(_ title: String, on: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).themeText(.subline, color: on ? Theme.textPrimary : Theme.textSecondary)
                .padding(.horizontal, 12).frame(minHeight: 36)
                .background(on ? Theme.borderStrong : Theme.background, in: Capsule())
                .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(on ? .isSelected : [])
    }
}

/// The sheet's title, the trip's one-line summary, Edit, and what goes under them (the day chips). At accessibility text sizes Edit drops under the
/// title instead of squeezing it.
struct PlanSheetHeader<Extra: View>: View {
    let title: String
    let summary: String?
    let onEdit: () -> Void
    @ViewBuilder var extra: () -> Extra
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            let titles = VStack(alignment: .leading, spacing: 2) {
                Text(title).themeText(.title, color: Theme.textPrimary).lineLimit(3).fixedSize(horizontal: false, vertical: true)
                if let summary { Text(summary).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true) }
            }
            let edit = Button("Edit", action: onEdit).font(Theme.sans(ThemeTokens.typeValue, .medium, relativeTo: .body)).tint(Theme.accent)
                .frame(minWidth: 44, minHeight: 44)
                .accessibilityHint("Change the trip or open a saved one")
            if typeSize.isAccessibilitySize {
                titles
                edit
            } else {
                HStack(alignment: .firstTextBaseline) { titles; Spacer(minLength: 8); edit }
            }
            extra()
        }
    }
}

typealias PlanSheetDetent = SheetDetent

enum PlanSheetLogic {
    /// What a long press on the map drops.
    static let droppedName = "A place you chose"

    /// What adding a charger as a stop does, and does not, mean.
    static let passThrough = "The route passes through it. This doesn't promise a charge there."

    /// "250 kW · Example Network · about 3 mi off the route": power and the detour, never whether the charger is free.
    static func chargerDetail(_ c: AlongCharger, offRoute: String?) -> String {
        var parts: [String] = []
        if let kw = c.maxPowerKW, kw > 0 { parts.append("\(Int(kw.rounded())) kW") }
        if let n = c.evNetwork, !n.isEmpty { parts.append(n) }
        if let offRoute { parts.append(offRoute) }
        return parts.isEmpty ? (c.name == droppedName ? "A pin you dropped" : "Fast charger") : parts.joined(separator: " · ")
    }

    /// "Seattle → Spokane": the sheet's title (the start is the truck's own place when none was picked).
    static func title(origin: String?, destination: String?) -> String {
        let to = destination.flatMap { $0.isEmpty ? nil : shortPlace($0) } ?? "Your trip"
        guard let origin, !origin.isEmpty else { return "To \(to)" }
        return "\(shortPlace(origin)) → \(to)"
    }

    /// The first part of a place line ("Seattle, Washington, United States" → "Seattle").
    static func shortPlace(_ line: String) -> String {
        line.split(separator: ",").first.map { $0.trimmingCharacters(in: .whitespaces) } ?? line
    }
}

/// The map's layer chips. "Fast chargers" (100 kW and up, near the route) shows them on the map; tapping one opens its callout. Hotels and
/// campgrounds join when the server can list them along a route.
struct PlanLayerChips: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel

    var body: some View {
        if model.along != .unavailable {
            let on = model.along != .off
            Button { Task { await model.toggleAlong(on: !on, source: state.client) } } label: {
                HStack(spacing: 6) {
                    if model.along == .loading { ProgressView().controlSize(.small).tint(Theme.textSecondary) }
                    Text(label).themeText(.subline, color: on ? Theme.textPrimary : Theme.textSecondary)
                }
                .padding(.horizontal, 12).frame(minHeight: 36)
                .background(on ? Theme.borderStrong : Theme.card.opacity(0.92), in: Capsule())
                .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
            }
            .buttonStyle(.plain)
            .disabled(model.along == .loading)
            .accessibilityLabel("Fast chargers along the route")
            .accessibilityValue(on ? "shown" : "hidden")
            .accessibilityAddTraits(on ? .isSelected : [])
        }
    }

    private var label: String {
        switch model.along {
        case .shown(let c): return "Fast chargers · \(c.count)"
        case .failed: return "Fast chargers (try again)"
        default: return "Fast chargers"
        }
    }
}

/// What a tap on a charger on the map shows: its name, power and network, how far it is off the route, and "Add as a stop". A stop the person adds is
/// a place the route passes through; the planner decides where to charge, and nothing here says the charger is free or working.
struct PlanPinCallout: View {
    let charger: AlongCharger
    let offRoute: String?
    let adding: Bool
    let onAdd: () -> Void
    let onClose: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(charger.name ?? "Fast charger").themeText(.value, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true)
                    Text(PlanSheetLogic.chargerDetail(charger, offRoute: offRoute)).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 8)
                Button(action: onClose) { Image(systemName: "xmark").frame(width: 44, height: 44) }
                    .buttonStyle(.plain).foregroundStyle(Theme.textSecondary)
                    .accessibilityLabel("Close")
            }
            Button(action: onAdd) { Text(adding ? "Adding…" : "Add as a stop").frame(maxWidth: .infinity) }
                .buttonStyle(ThemeButtonStyle()).disabled(adding)
                .accessibilityLabel("Add \(charger.name ?? "this charger") as a stop")
            Text(PlanSheetLogic.passThrough).themeText(.caption, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(12)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .shadow(color: .black.opacity(0.25), radius: 10, y: 2)
        .accessibilityElement(children: .contain)
    }
}
