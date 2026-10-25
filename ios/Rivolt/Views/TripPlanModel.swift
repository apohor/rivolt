import Foundation
import Observation

/// The calls behind the planner screen, kept apart from `APIClient` so a test can see what is sent.
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

    /// The two-day version of a long drive: choosing where to sleep, then the days.
    enum NightState: Equatable {
        case none
        case looking
        case choose([OvernightCandidates.Candidate])
        case planning
        case planned(TripMultiday)
        case failed(String)
    }

    private(set) var night = NightState.none
    /// The charging stop (index in the plan's stops) a tap on the map pointed at; the list scrolls to it.
    var focusedStop: Int?
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

    func search(source: some TripSource, query: String) async {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard q.count >= 3, !searching else { return }
        searching = true
        defer { searching = false }
        searchMessage = nil
        do {
            results = try await source.geocode(q, count: 6)
            if results.isEmpty { searchMessage = "No match for that. Try a city, or a street and a city." }
        } catch {
            if Task.isCancelled { return }
            results = []
            searchMessage = TripPlanLogic.failure(error)
        }
    }

    func clearSearch() {
        results = []
        searchMessage = nil
    }

    var canPlan: Bool { destination != nil && state != .planning }

    func plan(source: some TripSource) async {
        guard let destination, state != .planning else { return }
        state = .planning
        night = .none
        focusedStop = nil
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
        let body = TripPlanLogic.request(
            vehicleID: vehicleID, origin: start, destination: (destination.latitude, destination.longitude),
            startingSoc: useTruckCharge ? nil : startingSoc, departAt: leaveNow ? nil : departAt, packKwh: packKwh)
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
            let found = try await source.overnightCandidates(TripPlanLogic.overnightRequest(route))
            if found.target?.reachesEnd == true {
                night = .failed("This trip fits in one day.")
            } else if found.candidates.isEmpty {
                night = .failed("No hotel or campground with a listed charger near where the first day would end.")
            } else {
                night = .choose(Array(found.candidates.prefix(5)))
            }
        } catch {
            if Task.isCancelled { night = .none; return }
            night = .failed(TripPlanLogic.failure(error))
        }
    }

    /// Plans two days with the night at `stay`.
    func planNight(source: some TripSource, at stay: OvernightCandidates.Candidate) async {
        guard let destination, let start = lastStart else { return }
        night = .planning
        chosenStay = stay
        let body = TripPlanLogic.multidayRequest(
            vehicleID: vehicleID, startingSoc: useTruckCharge ? nil : startingSoc, packKwh: packKwh, startAt: lastLeave,
            origin: start, destination: (destination.latitude, destination.longitude), stay: stay)
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
        state = .planned(r.plan)
        night = r.multiday.map { .planned($0) } ?? .none
    }

    /// What to save for the trip on screen; nil until there is a plan and a place to leave from.
    func savedInputs() -> JSONValue? {
        guard let destination, let start = lastStart else { return nil }
        let from = origin?.line ?? "Where the truck was"
        return SavedTripLogic.inputs(
            origin: (start.lat, start.lon, from), destination: (destination.latitude, destination.longitude, destination.line),
            stay: planned2Days ? chosenStay : nil, startingSoc: useTruckCharge ? nil : startingSoc, departAt: leaveNow ? nil : departAt)
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
