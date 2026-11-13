import Foundation
import Observation

/// The calls behind the planner screen, kept apart from `APIClient` so a test can see what is sent.
/// The chargers-along call, kept apart from `APIClient` so a test can see what is sent.
protocol ChargersAlongSource: Sendable {
    func chargersAlong(_ body: ChargersAlongRequest) async throws -> ChargersAlong
}

extension APIClient: ChargersAlongSource {}

protocol WhatIfSource: Sendable {
    func whatIf(_ body: WhatIfRequest) async throws -> WhatIfResponse
}

extension APIClient: WhatIfSource {}

protocol CorridorLayersSource: Sendable {
    func corridorLayers(_ body: CorridorLayersRequest) async throws -> CorridorLayers
}

extension APIClient: CorridorLayersSource {}

/// The layers the planner map can show along the route.
enum MapLayer: String, CaseIterable, Equatable {
    case chargers, medium, hotels, camps

    /// The layer for a server kind ("dcfc", "l2", "hotels", "camp"); nil for one this app does not know.
    init?(kind: String) {
        guard let layer = Self.allCases.first(where: { $0.kind == kind }) else { return nil }
        self = layer
    }

    var kind: String {
        switch self {
        case .chargers: return "dcfc"
        case .medium: return "l2"
        case .hotels: return "hotels"
        case .camps: return "camp"
        }
    }
    /// The chip's word: the same charger-type words as everywhere (Fast, Medium, Hotel, Camping).
    var title: String {
        switch self {
        case .chargers: return ChargerLabels.label(.fast)
        case .medium: return ChargerLabels.label(.medium)
        case .hotels: return ChargerLabels.label(.hotel)
        case .camps: return ChargerLabels.label(.camping)
        }
    }
}

protocol TripSource: Sendable {
    func planTrip(_ body: TripPlanRequest) async throws -> TripPlan
    func geocode(_ q: String, count: Int) async throws -> [PlaceMatch]
    func vehicleState(id: String) async throws -> VehicleState
    func truckPosition(id: String) async throws -> TruckPosition
    func planMultiday(_ body: TripMultidayRequest) async throws -> TripMultiday
    func overnightCandidates(_ body: OvernightCandidatesRequest) async throws -> OvernightCandidates
}

extension APIClient: TripSource {}

/// One outing: where from (the truck, unless a place is picked), where to, the charge to start with, and when to leave. Planning
/// asks the server, which asks Rivian, so it can take a few seconds and can be refused (Rivian to reconnect, an hourly allowance);
/// each is said in plain words and nothing is retried on its own. The answer shown is the server's own.
@MainActor
@Observable
final class TripPlanModel {
    enum PlanState: Equatable {
        case idle
        case planning
        case planned(TripPlan)
        case failed(String)
    }

    var destination: PlaceMatch?
    /// nil: where the truck is.
    var origin: PlaceMatch?
    var useTruckCharge = true
    var startingSoc = 80.0
    var leaveNow = true
    /// The charge to arrive with, asked of the planner (the web's "Target arrival SoC %"), starting at its 20.
    var arrivalTarget = TripPlanLogic.arrivalTargetPercent
    /// The target the plan on screen was made with, so its warning matches what was asked even if the control moves after.
    private(set) var plannedTarget = TripPlanLogic.arrivalTargetPercent
    var departAt = Date().addingTimeInterval(3600)

    private(set) var vehicleID: String?
    private(set) var vehicleName: String?
    /// The truck's charge and pack right now, when known.
    private(set) var liveSoc: Double?
    private(set) var packKwh: Double?
    private(set) var state = PlanState.idle
    private(set) var results: [PlaceMatch] = []
    private(set) var searchMessage: String?
    private(set) var searching = false
    /// Which search owns `results`: a late answer from an older one is dropped.
    private var searchGeneration = 0

    /// The two-day version of a long drive: choosing where to sleep, then the days.
    enum NightState: Equatable {
        case none
        case looking
        case choose([OvernightCandidates.Candidate])
        /// Hotels with no charger listed, offered only when none with one is in range (the answer keeps the OpenStreetMap credit).
        case chooseFallback(OvernightCandidates)
        case planning
        case planned(TripMultiday)
        case failed(String)
    }

    private(set) var night = NightState.none
    /// The charging stop (index in the plan's stops) a tap on the map pointed at; the list scrolls to it.
    var focusedStop: Int?
    /// Where the elevation chart's cursor is, 0 to 1 along the route (the map shows a dot there); nil: none. Cleared when the plan changes.
    var elevationCursor: Double?
    /// The road line of the plan on screen, once the map has it: the better path for finding a place to sleep.
    var roadLine: [RouteSnap.Coord] = []

    /// Fast chargers along the plan on screen, when the person asked to see them. `nil` until asked; the server may have no listing.
    enum AlongState: Equatable {
        case off
        case loading
        case shown([AlongCharger])
        case failed(String)
        case unavailable
    }

    /// The places the person added for the route to pass through (a charger picked on the map). New trips start with none.
    private(set) var vias: [PlanVia] = []
    /// The stops of a re-plan under way, so the map can draw its own line through them at once.
    private(set) var pendingVias: [PlanVia]?
    /// A place the person dropped a pin on (a long press): a pin and a callout with Add as a stop, until they add it or close it.
    private(set) var dropped: AlongCharger?
    /// A re-plan with a changed set of stops is under way: the old route stays on the map until the new one arrives.
    private(set) var updating = false
    /// Said when the last change could not be planned (the route is unchanged).
    private(set) var updateProblem: String?

    private(set) var along = AlongState.off
    var chargers: [AlongCharger] { if case .shown(let c) = along { return c } else { return [] } }

    func toggleAlong(on: Bool, source: some ChargersAlongSource) async {
        guard on else { along = .off; if let f = focusedStop, f >= pinCount { focusedStop = nil; dropped = nil }; return }
        guard let route = plan?.best, along != .loading else { return }
        along = .loading
        do {
            along = .shown(try await source.chargersAlong(TripPlanLogic.alongRequest(route, roadLine: roadLine)).chargers)
        } catch {
            if Task.isCancelled { along = .off; return }
            if case APIError.server(404, _, _) = error { along = .unavailable } else { along = .failed(TripPlanLogic.failure(error)) }
        }
    }

    /// Adds a charger picked on the map as a stop on the route, and plans again. The route on screen stays until the new one comes.
    func addVia(_ c: AlongCharger, source: some TripSource) async {
        let v = PlanVia(lat: c.lat, lon: c.lon, name: c.name ?? "Fast charger", entityID: nil)
        guard !vias.contains(where: { $0.id == v.id }) else { return }
        closeCallout()
        await replan(adding: vias + [v], source: source)
    }

    /// Plans the same trip again with the same stops (after a setting the planner reads changed, such as the preferred networks).
    func replanKeepingStops(source: some TripSource) async {
        await replan(adding: vias, source: source)
    }

    func removeVia(_ id: String, source: some TripSource) async {
        await replan(adding: vias.filter { $0.id != id }, source: source)
    }

    /// The person put their stops in an order of their own: the route visits them as listed instead of by where they fall along the road.
    private(set) var customOrder = false

    /// Moves one of the person's stops a place earlier (-1) or later (1) in the order the route visits them, and plans again.
    func reorderVia(_ id: String, by step: Int, source: some TripSource) async {
        guard let next = TripPlanLogic.moved(vias, id: id, by: step) else { return }
        await replan(adding: next, keepOrder: true, source: source)
    }

    /// Plans the same trip with another set of stops, keeping the plan on screen until the answer is in; if it cannot be planned, the plan and
    /// the stops stay as they were and the problem is said.
    private func replan(adding next: [PlanVia], keepOrder: Bool = false, source: some TripSource) async {
        guard let destination, let start = lastStart, !updating, plan != nil else { return }
        updating = true
        updateProblem = nil
        let ordered = keepOrder || customOrder ? next : TripPlanLogic.orderedVias(next, along: roadLine)
        pendingVias = ordered
        defer { updating = false; pendingVias = nil }
        let body = TripPlanLogic.request(
            vehicleID: vehicleID, origin: start, destination: (destination.latitude, destination.longitude),
            startingSoc: useTruckCharge ? nil : startingSoc, departAt: lastLeave > Date() ? lastLeave : nil, packKwh: packKwh, target: plannedTarget, vias: ordered)
        do {
            let answer = try await TripPlanLogic.retryOnceAfterReconnect { try await source.planTrip(body) }
            guard answer.best != nil else { updateProblem = "The planner found no route with that stop. The route is unchanged."; return }
            vias = ordered
            if keepOrder { customOrder = true }
            night = .none
            roadLine = []
            dropped = nil
            clearPreview()
            state = .planned(answer)
        } catch {
            if Task.isCancelled { return }
            updateProblem = "That stop couldn't be planned: \(TripPlanLogic.failure(error)) The route is unchanged."
        }
    }

    // MARK: What-if preview of adding a stop

    enum PreviewState: Equatable { case idle, loading, shown(WhatIfResponse), unavailable, limited, failed }
    private(set) var preview = PreviewState.idle
    /// The edited stretch of the preview (our own routing), drawn on the map in place of nothing until the planner's route replaces the plan.
    private(set) var previewLine: [RouteSnap.Coord] = []
    private var previewTask: Task<Void, Never>?

    /// A pin or charger was picked: after a pause of 300 ms, ask what adding it as a stop would do (one request in flight, the last one wins). No
    /// call to Rivian; the plan itself is only asked for when the stop is added.
    func schedulePreview(for c: AlongCharger, source: some WhatIfSource) {
        guard let route = plan?.best,
              let body = WhatIfLogic.addRequest(route, point: RouteSnap.Coord(lat: c.lat, lon: c.lon), line: roadLine, target: plannedTarget) else { clearPreview(); return }
        schedulePreview(body, source: source)
    }

    private func schedulePreview(_ body: WhatIfRequest, source: some WhatIfSource) {
        previewTask?.cancel()
        previewLine = []
        preview = .loading
        previewTask = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(300))
            guard !Task.isCancelled else { return }
            do {
                let r = try await source.whatIf(body)
                guard !Task.isCancelled else { return }
                self?.preview = .shown(r)
                self?.previewLine = r.geometry.map { DriveRoute.decode($0, precision: 6) } ?? []
            } catch {
                guard !Task.isCancelled else { return }
                if case APIError.server(404, _, _) = error { self?.preview = .unavailable }
                else if case APIError.server(429, _, _) = error { self?.preview = .limited }
                else { self?.preview = .failed }
            }
        }
    }

    // MARK: Moving one of the person's own stops

    /// The stop being moved, and where the crosshair at the middle of the map has it now. The plan is asked for only on Done.
    private(set) var moving: PlanVia?
    private(set) var moveTo: RouteSnap.Coord?

    func startMove(_ v: PlanVia) {
        guard plan != nil, !updating else { return }
        closeCallout()
        moving = v
        moveTo = nil
    }

    func cancelMove() {
        moving = nil
        moveTo = nil
        clearPreview()
    }

    /// The map's middle moved: the stop follows it, and after a pause the preview says what that would add.
    func moveCenter(_ c: RouteSnap.Coord, source: some WhatIfSource) {
        guard let v = moving else { return }
        moveTo = c
        guard let route = plan?.best, let body = WhatIfLogic.moveRequest(route, via: v, to: c, target: plannedTarget) else { clearPreview(); return }
        schedulePreview(body, source: source)
    }

    /// Done: plan the trip again with the stop at the crosshair (the one call to the planner; the preview was only an estimate).
    func confirmMove(source: some TripSource) async {
        guard let m = moving, let to = moveTo else { cancelMove(); return }
        let next = vias.map { $0.id == m.id ? PlanVia(lat: to.lat, lon: to.lon, name: m.name, entityID: nil) : $0 }
        moving = nil
        moveTo = nil
        await replan(adding: next, source: source)
    }

    func clearPreview() {
        previewTask?.cancel()
        preview = .idle
        previewLine = []
    }

    // MARK: Map layers (corridor layers)

    /// The layer chips that are on. Each toggles on its own; Fast is on to begin with, and with every one off there are no pins and no request.
    /// The Weather toggle on the map: on to begin with (the planner settings will set its default). The tags in the itinerary stay whatever it says.
    var weatherOn = true
    private(set) var layers: Set<MapLayer> = [.chargers]
    /// Show only the fast chargers of the networks the person prefers (hotels and campgrounds are unaffected).
    private(set) var preferredOnly = false
    /// The slugs of the networks marked "Prefer in planner" in Settings.
    private(set) var preferredSlugs: Set<String> = []
    enum LayerState: Equatable { case idle, loading, shown, failed(String), unavailable }
    private(set) var layerState = LayerState.idle
    private(set) var layerPOIs: [CorridorPOI] = []
    private(set) var layerTotal = 0
    /// When the listing was built (the archive's date), not live; empty when unknown.
    private(set) var layerAsOf = ""

    func setPreferred(_ slugs: Set<String>) {
        preferredSlugs = slugs
        // "Preferred only" is on from the start in the person's settings: it takes hold once the networks are known (once, so a change on the map stays).
        if wantsPreferredOnly, !slugs.isEmpty {
            wantsPreferredOnly = false
            if layers.contains(.chargers) { preferredOnly = true }
        }
    }

    /// The Trip planner settings: read once, before the first search. They say which layers are on to begin with, whether "Preferred only" is, and
    /// how the search is made (distance from the route, hiding the first miles, the smallest fast charger). Where they cannot be read, or the server
    /// has none, the defaults stand and the search carries none of them.
    private(set) var plannerMap: PlannerMapSettings?
    private var prefsLoaded = false
    private var wantsPreferredOnly = false
    /// Whether the line the layers follow starts at the trip's own origin (the whole trip, or Day 1): only then are the first miles hidden.
    private var lineFromOrigin = true

    func loadPlannerPrefs(source: some PlannerPrefsSource) async {
        guard !prefsLoaded else { return }
        prefsLoaded = true
        guard let prefs = try? await source.plannerPrefs(), let m = PlannerMapSettings(prefs) else { return }
        plannerMap = m
        weatherOn = m.weatherOn
        layers = Set(m.layersOn.compactMap(MapLayer.init(kind:)))
        wantsPreferredOnly = m.preferredOnly
        if wantsPreferredOnly, !preferredSlugs.isEmpty, layers.contains(.chargers) { preferredOnly = true; wantsPreferredOnly = false }
    }

    /// A route arrived or changed: the settings first (once), then the search.
    func prepareLayers(source: some CorridorLayersSource & PlannerPrefsSource) async {
        await loadPlannerPrefs(source: source)
        await refreshLayers(source: source)
    }

    func setPreferredOnly(_ on: Bool) { preferredOnly = on && !preferredSlugs.isEmpty }

    func setLayer(_ layer: MapLayer, on: Bool, source: some CorridorLayersSource) async {
        if on { layers.insert(layer) } else { layers.remove(layer) }
        if layer == .chargers, !on { preferredOnly = false }
        await refreshLayers(source: source)
    }

    /// Searches along the plan's route for the layers that are on (Fast is on from the start): called when a route arrives or changes. Nothing is
    /// asked when every layer is off.
    func reloadLayers(source: some CorridorLayersSource) async {
        await refreshLayers(source: source)
    }

    /// Counts the layer searches: only the latest one's answer is shown, so a slow search along an older line can't land over a newer one.
    private var layerSearch = 0

    private func refreshLayers(source: some CorridorLayersSource) async {
        if let f = focusedStop, f >= pinCount { focusedStop = nil; dropped = nil }
        layerSearch += 1
        let search = layerSearch
        guard !layers.isEmpty else { layerPOIs = []; layerState = .idle; return }
        guard let route = plan?.best else { layerPOIs = []; layerState = .idle; return }
        layerState = .loading
        let line = TripPlanLogic.thin(corridorLine, to: 1500).map { [$0.lat, $0.lon] }
        let body = LayerSearch.request(path: line.count >= 2 ? line : TripPlanLogic.path(route), kinds: layers.map(\.kind).sorted(), settings: plannerMap, startsAtOrigin: lineFromOrigin)
        do {
            let answer = try await source.corridorLayers(body)
            guard search == layerSearch else { return }
            layerPOIs = answer.pois
            layerTotal = answer.total
            layerAsOf = answer.asof ?? ""
            layerState = .shown
        } catch {
            guard search == layerSearch else { return }
            if Task.isCancelled { layerState = .idle; return }
            if case APIError.server(404, _, _) = error { layerState = .unavailable } else { layerState = .failed(TripPlanLogic.failure(error)) }
        }
    }

    /// The layer's stations as map pins: a fast charger of a preferred network drawn prominent; with "Preferred only" the other fast chargers
    /// are left out.
    var layerMarkers: [AlongCharger] {
        layerPOIs.compactMap { p in
            let kinds = p.kinds ?? []
            let isCharger = kinds.contains("dcfc") || kinds.contains("l2")
            let isPreferred = p.networkSlug.map { preferredSlugs.contains($0) } ?? false
            if preferredOnly, isCharger, !kinds.contains("hotels"), !kinds.contains("camp"), !isPreferred { return nil }
            // A Level 2 station that is not DC is a Medium pin; a hotel or campground keeps its own.
            let kind = kinds.contains("camp") ? "camp" : kinds.contains("hotels") ? "hotel" : kinds.contains("dcfc") ? "dcfc" : "l2"
            return AlongCharger(lat: p.lat, lon: p.lon, name: p.name, isDCFC: p.isDCFC, maxPowerKW: p.maxPowerKW, evNetwork: p.evNetwork,
                                kind: kind, preferred: isPreferred, offKm: p.offKm, networkName: p.networkName)
        }
    }

    /// How many pins the map draws for stops: the charging stops, then the person's own (the markers are numbered after them).
    private var pinCount: Int {
        if case .planned(let trip) = night { return PlanRouteLogic.pinCount(days: trip.days) }
        return (plan?.best?.chargingStops.filter { $0.latitude != nil && $0.longitude != nil }.count ?? 0) + vias.count
    }

    /// Both days' road lines of a two-day trip (from the days map), what the layers are searched along then.
    private(set) var dayLines: [RouteSnap.Coord] = []

    /// The line the layers follow: a two-day trip's both days, else the plan's road line.
    private var corridorLine: [RouteSnap.Coord] {
        if case .planned(let trip) = night {
            if dayLines.count >= 2 { return dayLines }
            return trip.days.compactMap(\.plan.best).flatMap { PlanRouteLogic.places($0) }
        }
        return roadLine
    }

    /// The one-route map found its road line: kept for the overnight search and the callouts, and the layers (when on) are searched again along it.
    /// The same line again asks nothing.
    func setRoadLine(_ line: [RouteSnap.Coord], source: some CorridorLayersSource) async {
        guard line != roadLine else { return }
        roadLine = line
        if !layers.isEmpty { await refreshLayers(source: source) }
    }

    /// The days map found its road lines: the layers (when on) are searched again along the whole trip.
    func setDayLines(_ line: [RouteSnap.Coord], startsAtOrigin: Bool = true, source: some CorridorLayersSource) async {
        guard line != dayLines || startsAtOrigin != lineFromOrigin else { return }
        dayLines = line
        lineFromOrigin = startsAtOrigin
        if !layers.isEmpty { await refreshLayers(source: source) }
    }

    /// The chargers along the route, then a dropped pin: the pins the map draws after the stops.
    /// The pins past the stops: the older fast-charger list when it was asked for (the list-first plan screen), else the layers that are on.
    private var shownAlong: [AlongCharger] { chargers.isEmpty ? layerMarkers : chargers }

    var markers: [AlongCharger] { shownAlong + (dropped.map { [$0] } ?? []) }

    /// The charger (or dropped pin) a tap on the map picked, if the focus is past the stops.
    var pickedCharger: AlongCharger? {
        guard let f = focusedStop else { return nil }
        let j = f - pinCount
        return markers.indices.contains(j) && j >= 0 ? markers[j] : nil
    }

    /// A long press on the map: a pin there, picked, with its callout. Nothing is planned until the person adds it.
    func dropPin(lat: Double, lon: Double) {
        guard plan != nil, !updating else { return }
        dropped = AlongCharger(lat: lat, lon: lon, name: PlanSheetLogic.droppedName)
        focusedStop = pinCount + shownAlong.count
    }

    func closeCallout() {
        focusedStop = nil
        dropped = nil
        clearPreview()
    }
    /// Where the last plan started and left, kept so the two-day version is planned from the same place at the same time.
    private var lastStart: (lat: Double, lon: Double)?
    /// The stay the two-day version was planned around.
    private(set) var chosenStay: OvernightCandidates.Candidate?
    private var lastLeave = Date()

    var plan: TripPlan? { if case .planned(let p) = state { return p } else { return nil } }

    func loadTruck(source: some TripSource, vehicles: [Vehicle]) async {
        guard let v = vehicles.first else { return }
        vehicleID = v.id
        vehicleName = v.name
        if let s = try? await source.vehicleState(id: v.id) {
            liveSoc = s.batteryLevelPct
            packKwh = s.batteryCapacityKwh.flatMap { $0 > 0 ? $0 : nil }
        }
    }

    /// Type-ahead, as the web's LocationSearch: at least 2 characters, a short pause after the last keystroke, up to 8 matches, and the
    /// request of an earlier keystroke dropped (cancel the task; a newer search also wins over a late answer). Run it from `.task(id: query)`
    /// so the next keystroke cancels this one.
    func typeAhead(source: some TripSource, query: String, debounce: Duration = TripPlanLogic.typeAheadDebounce) async {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard q.count >= TripPlanLogic.typeAheadMinChars else {
            searchGeneration += 1
            searching = false
            clearSearch()
            return
        }
        searching = true
        try? await Task.sleep(for: debounce)
        if Task.isCancelled { return }
        await fetch(source: source, q: q)
    }

    /// The keyboard's Search key: the same search at once, without the pause.
    func search(source: some TripSource, query: String) async {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard q.count >= TripPlanLogic.typeAheadMinChars else { return }
        await fetch(source: source, q: q)
    }

    private func fetch(source: some TripSource, q: String) async {
        searchGeneration += 1
        let mine = searchGeneration
        searching = true
        searchMessage = nil
        do {
            let found = try await source.geocode(q, count: TripPlanLogic.typeAheadCount)
            guard mine == searchGeneration, !Task.isCancelled else { return }
            results = found
            if found.isEmpty { searchMessage = "No match for that. Try a city, or a street and a city." }
        } catch {
            guard mine == searchGeneration, !Task.isCancelled else { return }
            results = []
            searchMessage = TripPlanLogic.searchFailure(error)
        }
        searching = false
    }

    func clearSearch() {
        searchGeneration += 1
        searching = false
        results = []
        searchMessage = nil
    }

    var canPlan: Bool { destination != nil && state != .planning }

    func plan(source: some TripSource) async {
        guard let destination, state != .planning else { return }
        state = .planning
        night = .none
        focusedStop = nil
        roadLine = []
        along = .off
        vias = []
        customOrder = false
        updateProblem = nil
        let start: (lat: Double, lon: Double)
        if let origin {
            start = (origin.latitude, origin.longitude)
        } else {
            guard let id = vehicleID, let p = try? await source.truckPosition(id: id), let c = p.coordinate else {
                state = .failed("The truck hasn't reported where it is. Pick a starting place.")
                return
            }
            start = (c.latitude, c.longitude)
        }
        lastStart = start
        lastLeave = leaveNow ? Date() : departAt
        plannedTarget = arrivalTarget
        let body = TripPlanLogic.request(
            vehicleID: vehicleID, origin: start, destination: (destination.latitude, destination.longitude),
            startingSoc: useTruckCharge ? nil : startingSoc, departAt: leaveNow ? nil : departAt, packKwh: packKwh, target: plannedTarget)
        do {
            let plan = try await TripPlanLogic.retryOnceAfterReconnect { try await source.planTrip(body) }
            state = plan.best == nil ? .failed("The planner found no route for that trip.") : .planned(plan)
        } catch {
            if Task.isCancelled { state = .idle; return }
            state = .failed(TripPlanLogic.failure(error))
        }
    }

    /// The daily limit changed: a list of stays found for the old one no longer fits.
    func drivingLimitChanged() {
        if night != .looking { night = .none }
    }

    /// Looks for stays near where a day's driving runs out, for the person to choose from.
    func findNight(source: some TripSource) async {
        guard let route = plan?.best, night == .none || isFailedNight else { return }
        night = .looking
        do {
            let found = try await source.overnightCandidates(TripPlanLogic.overnightRequest(route, roadLine: roadLine))
            if found.target?.reachesEnd == true {
                night = .failed("This trip fits in one day.")
            } else if found.candidates.isEmpty, !found.fallbackStays.isEmpty {
                var shown = found
                shown.fallbackStays = Array(found.fallbackStays.prefix(5))
                night = .chooseFallback(shown)
            } else if found.candidates.isEmpty {
                night = .failed(TripPlanLogic.noStaysMessage(found, driveSec: route.totalDriveDurationSec, metric: NowFormat.usesMetric))
            } else {
                night = .choose(Array(found.candidates.prefix(5)))
            }
        } catch {
            if Task.isCancelled { night = .none; return }
            night = .failed(TripPlanLogic.failure(error))
        }
    }

    /// Plans two days with the night at `stay`.
    func planNight(source: some TripSource, at stay: OvernightCandidates.Candidate, morning: OvernightCandidates.FastCharger? = nil) async {
        guard let destination, let start = lastStart else { return }
        night = .planning
        chosenStay = stay
        let body = TripPlanLogic.multidayRequest(
            vehicleID: vehicleID, startingSoc: useTruckCharge ? nil : startingSoc, packKwh: packKwh, startAt: lastLeave,
            origin: start, destination: (destination.latitude, destination.longitude), stay: stay, morningCharger: morning,
            target: plannedTarget)
        do {
            let result = try await TripPlanLogic.retryOnceAfterReconnect { try await source.planMultiday(body) }
            night = result.days.isEmpty ? .failed("The planner found no route for that trip.") : .planned(result)
            focusedStop = nil
        } catch {
            if Task.isCancelled { night = .none; return }
            night = .failed(TripPlanLogic.failure(error))
        }
    }

    /// Back to one day. The pins are numbered differently on the one route than on the two days, so a picked pin is let go either way.
    func clearNight() { night = .none; chosenStay = nil; focusedStop = nil }

    /// Shows a saved trip as it was saved, with no planner call. The form takes its places, so planning again starts from them.
    func open(_ r: SavedTripLogic.Restored) {
        origin = r.origin
        destination = r.destination
        lastStart = (r.origin.latitude, r.origin.longitude)
        focusedStop = nil
        chosenStay = r.stay
        if let t = r.target { arrivalTarget = TripPlanLogic.clampedTarget(t); plannedTarget = t }
        state = .planned(r.plan)
        night = r.multiday.map { .planned($0) } ?? .none
    }

    /// What to save for the trip on screen; nil until there is a plan and a place to leave from.
    func savedInputs() -> JSONValue? {
        guard let destination, let start = lastStart else { return nil }
        let from = origin?.line ?? "Where the truck was"
        return SavedTripLogic.inputs(
            origin: (start.lat, start.lon, from), destination: (destination.latitude, destination.longitude, destination.line),
            stay: planned2Days ? chosenStay : nil, startingSoc: useTruckCharge ? nil : startingSoc, departAt: leaveNow ? nil : departAt,
            target: plannedTarget)
    }

    func savedPlan() -> JSONValue? {
        if case .planned(let trip) = night { return SavedTripLogic.planBlob(plan: plan, multiday: trip) }
        return SavedTripLogic.planBlob(plan: plan, multiday: nil)
    }

    var planned2Days: Bool { if case .planned = night { return true } else { return false } }

    /// "Seattle to Spokane", for a name field.
    var suggestedName: String? {
        destination.map { SavedTripLogic.suggestedName(origin: origin?.line, destination: $0.line) }
    }

    private var isFailedNight: Bool { if case .failed = night { return true } else { return false } }
}


/// The layer search as the settings shape it, apart from the model so a test can read what is sent.
enum LayerSearch {
    /// Whether the line searched starts at the trip's own origin, the only place the "hide chargers near the start" setting applies (the person starts charged
    /// there). False for Day 2 and later (they start at an overnight stop) and for a re-plan from the truck's position partway through a trip (the web has
    /// one: you are not leaving charged). The iPhone has no re-plan from the truck's position today; whoever adds one must pass `replanFromTruck: true`.
    static func startsAtOrigin(day: Int?, replanFromTruck: Bool = false) -> Bool {
        !replanFromTruck && (day == nil || day == 1)
    }

    /// The request for `kinds` along `path`. With the person's settings it carries the three that shape the search; `startsAtOrigin` is false for a line that
    /// starts somewhere other than the trip's origin (Day 2 and later start at an overnight stop), and then nothing near its start is hidden.
    static func request(path: [[Double]], kinds: [String], settings: PlannerMapSettings?, startsAtOrigin: Bool) -> CorridorLayersRequest {
        guard let settings else { return CorridorLayersRequest(path: path, kinds: kinds, limit: nil) }
        return CorridorLayersRequest(path: path, kinds: kinds, limit: nil, preset: settings.corridor,
                                     startExcludeMi: startsAtOrigin ? settings.startExcludeMi : 0, minFastKw: settings.minFastKw)
    }
}

protocol PlannerPrefsSource: Sendable {
    func plannerPrefs() async throws -> PlannerPrefs
}

extension APIClient: PlannerPrefsSource {}
