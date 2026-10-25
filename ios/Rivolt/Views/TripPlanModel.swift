import Foundation
import Observation

/// The calls behind the planner screen, kept apart from `APIClient` so a test can see what is sent.
protocol TripSource: Sendable {
    func planTrip(_ body: TripPlanRequest) async throws -> TripPlan
    func geocode(_ q: String, count: Int) async throws -> [PlaceMatch]
    func vehicleState(id: String) async throws -> VehicleState
    func truckPosition(id: String) async throws -> TruckPosition
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
}
