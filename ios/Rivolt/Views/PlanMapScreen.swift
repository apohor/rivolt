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
    @State private var showingNetworks = false
    @Environment(\.dynamicTypeSize) private var typeSize
    /// At accessibility text sizes the sheet's header would cover the map, so it is only the title; the rest sits at the top of the sheet's own content.
    private var ax: Bool { typeSize.isAccessibilitySize }
    /// What the sheet's header needs at this text size: the sheet is never shorter (see DetentSheet.height).
    @State private var headerNeed: CGFloat = 0

    private var metric: Bool { NowFormat.usesMetric }

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .bottom) {
                map(coverHeight: DetentSheet<EmptyView, EmptyView>.height(PlanSheetDetent.half, in: geo.size.height, floor: headerNeed))
                    .overlay(alignment: .topLeading) { overlays }
                if let v = model.moving {
                    // The stop follows the middle of the map: a crosshair there, and a bar to say what it does and to finish.
                    Image(systemName: "plus.circle").font(.system(size: 30, weight: .light)).foregroundStyle(Theme.accent)
                        .frame(maxHeight: .infinity, alignment: .center).allowsHitTesting(false).accessibilityHidden(true)
                    PlanMoveBar(name: v.name, lines: PlanSheetLogic.previewLines(model, metric: metric), previewing: model.preview == .loading,
                                onCancel: { model.cancelMove() }, onDone: { Task { await model.confirmMove(source: state.client) } })
                        .padding(.horizontal, 12).padding(.bottom, DetentSheet<EmptyView, EmptyView>.height(detent, in: geo.size.height, floor: headerNeed) + 10)
                        .frame(maxHeight: .infinity, alignment: .bottom)
                }
                if let c = model.pickedCharger {
                    PlanPinCallout(charger: c, offRoute: TripPlanLogic.offRouteLine(c.offKm.map { $0 * 1000 } ?? TripPlanLogic.offRouteMeters(lat: c.lat, lon: c.lon, line: model.roadLine), metric: metric),
                                   previewLines: PlanSheetLogic.previewLines(model, metric: metric), previewing: model.preview == .loading,
                                   adding: model.updating, onAdd: { Task { await model.addVia(c, source: state.client) } }, onClose: { model.closeCallout() })
                        .padding(.horizontal, 12)
                        .padding(.bottom, DetentSheet<EmptyView, EmptyView>.height(detent, in: geo.size.height, floor: headerNeed) + 10)
                        .frame(maxHeight: .infinity, alignment: .bottom)
                }
                DetentSheet(detent: $detent, container: geo.size.height, floor: headerNeed, scrollsAtEveryDetent: ax, onHeaderNeed: { headerNeed = $0 }) {
                    header
                } content: {
                    ScrollViewReader { proxy in
                        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                            if ax { axControls }
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
        // Large text: the map first, the sheet down to its title; the person opens it for the rest.
        .onAppear { if ax { detent = .peek } }
        .onChange(of: ax) { _, large in detent = large ? .peek : .half }
        .onChange(of: model.moving?.id) { _, new in if new != nil { detent = .peek } }
        .onChange(of: model.pickedCharger?.id) { _, new in
            if let c = model.pickedCharger, new != nil { model.schedulePreview(for: c, source: state.client) } else { model.clearPreview() }
        }
        .sheet(isPresented: $showingNetworks) {
            PlannerNetworksSheet { Task { await model.replanKeepingStops(source: state.client) } }.followsAppearance()
        }
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
            PlanDaysMap(days: trip.days, focus: model.focusedStop, onStop: { model.focusedStop = $0 }, along: model.markers,
                        onLine: { line in Task { await model.setDayLines(line, source: state.client) } }, fills: true, bottomInset: coverHeight, day: day)
        } else if let route = model.plan?.best {
            PlanRouteMap(route: route, onStop: { model.focusedStop = $0 }, focus: model.focusedStop, along: model.markers,
                         onLine: { model.roadLine = $0 }, fills: true, bottomInset: coverHeight,
                         onLongPress: { model.dropPin(lat: $0.lat, lon: $0.lon) },
                         onCenter: model.moving == nil ? nil : { model.moveCenter($0, source: state.client) },
                         vias: model.vias, pendingVias: model.pendingVias, previewLine: model.previewLine)
        }
    }

    @ViewBuilder
    private var header: some View {
        let title = PlanSheetLogic.title(origin: model.origin?.line, destination: model.destination?.line)
        if ax {
            // Only the title (two lines at most): the summary, Edit, the day chips and Networks are in the sheet below it.
            PlanSheetCompactHeader(title: title)
        } else {
            PlanSheetHeader(title: title, summary: model.plan?.best.map { TripPlanLogic.summary($0, metric: metric) }, onEdit: onEdit) {
                if case .planned(let trip) = model.night, trip.days.count > 1 { dayChips(trip) }
                networksButton
            }
        }
    }

    /// The trip's summary, Edit, the day chips and Networks, at the top of the sheet's content at accessibility sizes.
    private var axControls: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let summary = model.plan?.best.map({ TripPlanLogic.summary($0, metric: metric) }) {
                Text(summary).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
            }
            Button("Edit", action: onEdit).font(Theme.sans(ThemeTokens.typeValue, .medium, relativeTo: .body)).tint(Theme.accent)
                .frame(minWidth: 44, minHeight: 44, alignment: .leading)
                .accessibilityHint("Change the trip or open a saved one")
            if case .planned(let trip) = model.night, trip.days.count > 1 { dayChips(trip) }
            networksButton
        }
    }

    @ViewBuilder
    private var networksButton: some View {
        if !state.isDemo {
            Button { showingNetworks = true } label: {
                Text("Networks").themeText(.subline, color: Theme.textPrimary)
                    .padding(.horizontal, 12).frame(minHeight: 36)
                    .background(Theme.background, in: Capsule())
                    .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
            }
            .buttonStyle(.plain)
            .accessibilityHint("Pick the charging networks to prefer, then plan again")
        }
    }

    private func dayChips(_ trip: TripMultiday) -> some View {
        PlanDayChips(days: trip.days.map(\.index), day: $day)
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
    /// The line under the chips: what is loading, how many are shown, when the listing was built (never live), or what went wrong.
    @MainActor static func layerNote(_ model: TripPlanModel) -> String? {
        switch model.layerState {
        case .idle, .unavailable: return nil
        case .loading: return "Looking along the route…"
        case .failed(let words): return words
        case .shown:
            var parts = [model.layerPOIs.count < model.layerTotal ? "Showing \(model.layerPOIs.count) of \(model.layerTotal)" : "\(model.layerMarkers.count) along the route"]
            if !model.layerAsOf.isEmpty { parts.append("listing from \(model.layerAsOf)") }
            parts.append("not live")
            return parts.joined(separator: " · ")
        }
    }

    /// The preview's lines for the picked pin (empty when it has not come, is not available, or the server is busy).
    @MainActor static func previewLines(_ model: TripPlanModel, metric: Bool) -> [String] {
        switch model.preview {
        case .shown(let r):
            return WhatIfLogic.lines(r, metric: metric, zone: TripPlanLogic.zone(model.plan?.destinationTz) ?? .current)
        case .limited: return ["Too many previews just now. Adding the stop still works."]
        default: return []
        }
    }

    /// What a long press on the map drops.
    static let droppedName = "A place you chose"

    /// What adding a charger as a stop does, and does not, mean.
    static let passThrough = "The route passes through it. This doesn't promise a charge there."

    /// "250 kW · Example Network · about 3 mi off the route": power and the detour, never whether the charger is free.
    static func chargerDetail(_ c: AlongCharger, offRoute: String?) -> String {
        var parts: [String] = []
        let kw = c.maxPowerKW ?? 0
        switch c.kind {
        case "hotel": parts.append(ChargerLabels.label(.hotel) + (kw > 0 ? " · listed charger, up to \(Int(kw.rounded())) kW" : ""))
        case "camp": parts.append(ChargerLabels.label(.camping))
        default:
            // The station's rating, said as the station's, next to its type (Fast, or Medium for a Level 2 one).
            if let kind = ChargerLabels.kind(kw: kw, dc: c.isDCFC ?? (c.kind == "dcfc" ? true : nil)) { parts.append(ChargerLabels.stationLine(kind, ratedKw: kw)) }
        }
        if let n = c.networkName ?? c.evNetwork, !n.isEmpty { parts.append(n) }
        if c.preferred { parts.append("a preferred network") }
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

/// The map's layer chips: Chargers, Hotels and Campgrounds along the route (from the server's listing, not live), and, when the person has preferred
/// networks, Preferred only for the chargers. A tap on a pin opens its callout.
struct PlanLayerChips: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel

    var body: some View {
        if model.layerState != .unavailable {
            VStack(alignment: .leading, spacing: 6) {
                if typeSize.isAccessibilitySize {
                    layersMenu
                } else {
                // The chips wrap onto more rows rather than scroll sideways: every one is on screen and tappable at any text size (a scrolling row
                // clipped "Preferred only" on a phone and could not be swiped).
                FlowLayout(spacing: 8) {
                    ForEach(MapLayer.allCases, id: \.self) { layer in
                        chip(layer.title, on: model.layers.contains(layer)) { Task { await model.setLayer(layer, on: !model.layers.contains(layer), source: state.client) } }
                    }
                    if model.layers.contains(.chargers), !model.preferredSlugs.isEmpty {
                        chip("Preferred only", on: model.preferredOnly) { model.setPreferredOnly(!model.preferredOnly) }
                    }
                    ChargerTypeInfoButton(kinds: [.fast, .medium, .hotel, .camping])
                }
                }
                if let note = PlanSheetLogic.layerNote(model) {
                    LayerNotePill(text: note, loading: model.layerState == .loading)
                        // A note over the map stays small: the whole text is still its accessibility label.
                        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
                }
            }
            .task { await loadPreferred() }
            // Fast is on from the start: its pins are asked for when the route arrives, and again when the route changes.
            .task(id: model.plan?.best) { await model.reloadLayers(source: state.client) }
        }
    }

    @Environment(\.dynamicTypeSize) private var typeSize

    /// At accessibility text sizes the chips would fill the map: one "Layers" button instead, and a menu of the same switches, each with a check when on.
    private var layersMenu: some View {
        Menu {
            // Toggles, so VoiceOver says on or off for each, and the menu shows the checks itself.
            ForEach(MapLayer.allCases, id: \.self) { layer in
                Toggle(layer.title, isOn: Binding(
                    get: { model.layers.contains(layer) },
                    set: { on in Task { await model.setLayer(layer, on: on, source: state.client) } }))
            }
            if model.layers.contains(.chargers), !model.preferredSlugs.isEmpty {
                Toggle("Preferred only", isOn: Binding(get: { model.preferredOnly }, set: { model.setPreferredOnly($0) }))
            }
        } label: {
            // Not capped: this is the control for people who asked for larger text, and "Layers · 4" fits one line even at the largest size.
            Text(layersSummary).themeText(.subline, color: Color.white).lineLimit(1)
                .padding(.horizontal, 14).frame(minHeight: 44)
                .background(Theme.accentButton, in: Capsule())
        }
        .disabled(model.layerState == .loading)
        .accessibilityLabel("Map layers")
        .accessibilityValue(model.layers.isEmpty ? "none on" : MapLayer.allCases.filter { model.layers.contains($0) }.map(\.title).joined(separator: ", "))
    }

    /// "Layers" and how many are on, on one line.
    private var layersSummary: String {
        let n = MapLayer.allCases.filter { model.layers.contains($0) }.count
        return n == 0 ? "Layers" : "Layers · \(n)"
    }

    private func chip(_ title: String, on: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            // A chip that is on is filled solid (the accent button's emerald, white text), as the web's chips are; off is the card.
            Text(title).themeText(.subline, color: on ? Color.white : Theme.textSecondary)
                .padding(.horizontal, 12).frame(minHeight: 36)
                .background(on ? Theme.accentButton : Theme.card.opacity(0.92), in: Capsule())
                .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .disabled(model.layerState == .loading)
        .accessibilityLabel(title)
        .accessibilityValue(on ? "shown" : "hidden")
        .accessibilityAddTraits(on ? .isSelected : [])
    }

    /// The networks marked Prefer in planner, from the same list as Settings (read once; nothing if it can't be read).
    private func loadPreferred() async {
        guard model.preferredSlugs.isEmpty, let rows = try? await state.client.chargingNetworks() else { return }
        model.setPreferred(Set(rows.filter { $0.preferred == true }.compactMap { ($0.slug ?? "").isEmpty ? nil : $0.slug }))
    }
}

/// The line under the chips: what is loading, how many are shown, when the listing was built. At most two lines, so a large type size does not let it
/// cover the route; the whole text is the accessibility label.
struct LayerNotePill: View {
    let text: String
    var loading = false

    var body: some View {
        Text(text).themeText(.caption, color: loading ? Theme.textSecondary : Theme.textTertiary)
            .lineLimit(2).truncationMode(.tail)
            .accessibilityLabel(text)
            .padding(.horizontal, 8).padding(.vertical, 3)
            .background(Theme.card.opacity(0.9), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }
}

/// What a tap on a charger on the map shows: its name, power and network, how far it is off the route, and "Add as a stop". A stop the person adds is
/// a place the route passes through; the planner decides where to charge, and nothing here says the charger is free or working.
struct PlanPinCallout: View {
    let charger: AlongCharger
    let offRoute: String?
    /// What adding it would do (the what-if preview), as lines; empty until it arrives or where it is not available.
    var previewLines: [String] = []
    var previewing = false
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
            if previewing { HStack(spacing: 6) { ProgressView().controlSize(.small).tint(Theme.textSecondary); Text("Working out what it adds…").themeText(.caption, color: Theme.textSecondary) } }
            if !previewLines.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(previewLines.enumerated()), id: \.offset) { i, line in
                        Text(line).themeText(i == 0 ? .value : .caption, color: i == 0 ? Theme.textPrimary : (i == previewLines.count - 1 ? Theme.textTertiary : Theme.textSecondary))
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .accessibilityElement(children: .combine)
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

/// "Move a stop": the stop follows the crosshair at the middle of the map; this says what the move would add (an estimate) and has Cancel and Done.
/// Done is the only thing that asks the planner.
struct PlanMoveBar: View {
    let name: String
    let lines: [String]
    let previewing: Bool
    let onCancel: () -> Void
    let onDone: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Move \(name)").themeText(.value, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true)
            Text("Pan the map: the stop follows the cross.").themeText(.caption, color: Theme.textSecondary)
            if previewing { HStack(spacing: 6) { ProgressView().controlSize(.small).tint(Theme.textSecondary); Text("Working out what it adds…").themeText(.caption, color: Theme.textSecondary) } }
            if !lines.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { i, line in
                        Text(line).themeText(i == 0 ? .value : .caption, color: i == 0 ? Theme.textPrimary : (i == lines.count - 1 ? Theme.textTertiary : Theme.textSecondary))
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .accessibilityElement(children: .combine)
            }
            HStack(spacing: 8) {
                Button("Cancel", action: onCancel).buttonStyle(ThemeSecondaryButtonStyle())
                Button("Done", action: onDone).buttonStyle(ThemeButtonStyle())
            }
        }
        .padding(12)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .shadow(color: .black.opacity(0.25), radius: 10, y: 2)
        .accessibilityElement(children: .contain)
    }
}


/// The sheet's header at accessibility text sizes: the title, two lines at most. The rest (summary, Edit, the day chips, Networks) is at the top of the
/// sheet's content, so the header never takes the map.
struct PlanSheetCompactHeader: View {
    let title: String

    var body: some View {
        Text(title).themeText(.title, color: Theme.textPrimary).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityAddTraits(.isHeader)
    }
}

/// Whole trip and each day of a multi-day plan: chips that wrap onto more rows rather than scroll sideways, so none is clipped at any text size.
struct PlanDayChips: View {
    let days: [Int]
    @Binding var day: Int?

    var body: some View {
        FlowLayout(spacing: 8) {
            chip("Whole trip", on: day == nil) { day = nil }
            ForEach(days, id: \.self) { d in chip("Day \(d)", on: day == d) { day = d } }
        }
    }

    private func chip(_ title: String, on: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).themeText(.subline, color: on ? Color.white : Theme.textSecondary)
                .padding(.horizontal, 12).frame(minHeight: 44)
                .background(on ? Theme.accentButton : Theme.background, in: Capsule())
                .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(on ? .isSelected : [])
    }
}
