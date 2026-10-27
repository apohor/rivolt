import Foundation
import Observation

/// The calls behind the planner screen, kept apart from `APIClient` so a test can see what is sent.
/// The chargers-along call, kept apart from `APIClient` so a test can see what is sent.
protocol ChargersAlongSource: Sendable {
    func chargersAlong(_ body: ChargersAlongRequest) async throws -> ChargersAlong
}

extension APIClient: ChargersAlongSource {}

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

    private(set) var along = AlongState.off
    var chargers: [AlongCharger] { if case .shown(let c) = along { return c } else { return [] } }

    func toggleAlong(on: Bool, source: some ChargersAlongSource) async {
        guard on else { along = .off; if let f = focusedStop, f >= pinCount { focusedStop = nil }; return }
        guard let route = plan?.best, along != .loading else { return }
        along = .loading
        do {
            along = .shown(try await source.chargersAlong(TripPlanLogic.alongRequest(route, roadLine: roadLine)).chargers)
        } catch {
            if Task.isCancelled { along = .off; return }
            if case APIError.server(404, _) = error { along = .unavailable } else { along = .failed(TripPlanLogic.failure(error)) }
        }
    }

    /// How many charging-stop pins the map draws (the chargers along the route are numbered after them).
    private var pinCount: Int { plan?.best?.chargingStops.filter { $0.latitude != nil && $0.longitude != nil }.count ?? 0 }

    /// The charger a tap on the map picked, if the focus is past the stops.
    var pickedCharger: AlongCharger? {
        guard let f = focusedStop else { return nil }
        let j = f - pinCount
        return chargers.indices.contains(j) && j >= 0 ? chargers[j] : nil
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
            let plan = try await source.planTrip(body)
            state = plan.best == nil ? .failed("The planner found no route for that trip.") : .planned(plan)
        } catch {
            if Task.isCancelled { state = .idle; return }
            state = .failed(TripPlanLogic.failure(error))
        }
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
            let result = try await source.planMultiday(body)
            night = result.days.isEmpty ? .failed("The planner found no route for that trip.") : .planned(result)
        } catch {
            if Task.isCancelled { night = .none; return }
            night = .failed(TripPlanLogic.failure(error))
        }
    }

    func clearNight() { night = .none; chosenStay = nil }

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
