import Foundation
import Observation

/// The demo's sample trips, kept apart from `APIClient` so a test can see what is read.
protocol DemoTripsSource: Sendable {
    func demoTrips() async throws -> DemoTrips
    func demoTripPlan(id: String) async throws -> JSONValue
}

extension APIClient: DemoTripsSource {}

enum DemoTripLogic {
    /// "264 mi · 4 h 4 min · 1 charging stop · arrives with about 30%".
    static func summary(_ s: DemoTripPreset.Summary?, metric: Bool) -> String {
        guard let s else { return "" }
        var parts: [String] = []
        if let d = s.distanceMi { parts.append(MoneyLogic.distance(km: d * 1.609344, metric: metric)) }
        if let m = s.durationMin { parts.append(TripPlanLogic.duration(m * 60)) }
        if let n = s.chargeStops { parts.append(n == 0 ? "no charging stop" : "\(n) charging \(n == 1 ? "stop" : "stops")") }
        if let a = s.arrivalSocPct { parts.append("arrives with about \(Int(a.rounded()))%") }
        return parts.joined(separator: " · ")
    }

    /// A sample trip as the screen shows a saved one: its places from the preset, its plan as stored. The preset's route shape goes
    /// on a plan that has none, so the map follows the roads without asking a router (the demo is read only).
    static func restore(_ preset: DemoTripPreset, plan: JSONValue) -> SavedTripLogic.Restored? {
        guard var r = SavedTripLogic.restore(SavedTrip(id: preset.id, name: preset.name, inputs: preset.inputs, plan: plan)) else { return nil }
        if r.multiday == nil, r.plan.routes.indices.contains(0), r.plan.routes[0].routePolyline == nil {
            r.plan.routes[0].routePolyline = preset.routePolyline
        }
        return r
    }
}

/// The sample trips on the demo's Plan tab: the list, and opening one (its stored plan, no planner call).
@MainActor
@Observable
final class DemoTripsModel {
    enum Phase: Equatable {
        case loading
        case loaded([DemoTripPreset])
        case failed(String)
    }

    private(set) var phase = Phase.loading
    private(set) var opening: String?
    private(set) var message: String?
    private(set) var openedID: String?

    func load(source: some DemoTripsSource) async {
        do {
            phase = .loaded(try await source.demoTrips().trips)
        } catch {
            if Task.isCancelled { return }
            phase = .failed("Couldn't load the sample trips. Try again in a minute.")
        }
    }

    /// Shows a sample trip on `plan`. Nothing is sent but the read.
    func open(_ preset: DemoTripPreset, source: some DemoTripsSource, on plan: TripPlanModel) async {
        guard opening == nil else { return }
        opening = preset.id
        message = nil
        defer { opening = nil }
        do {
            let blob = try await source.demoTripPlan(id: preset.id)
            guard let restored = DemoTripLogic.restore(preset, plan: blob) else {
                message = "That sample trip can't be shown right now."
                return
            }
            plan.open(restored)
            openedID = preset.id
        } catch {
            if Task.isCancelled { return }
            message = "Couldn't open that sample trip. Try again in a minute."
        }
    }
}
