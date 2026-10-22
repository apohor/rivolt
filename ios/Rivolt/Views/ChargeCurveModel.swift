import Foundation
import Observation

/// The one read behind a charge's curve. Kept apart from `APIClient` so tests can see what is asked for.
protocol ChargeSamplesSource: Sendable {
    func chargeSamples(since: Date, until: Date, vehicleID: String, limit: Int) async throws -> [ChargeSample]
}

/// Loads one charge's samples when its screen opens and keeps them only as long as the screen: nothing is cached or
/// stored. A failed read says so; it is not retried on its own (a 429 stays a 429 until the screen is opened again).
@MainActor
@Observable
final class ChargeCurveModel {
    enum Phase: Equatable {
        case loading
        case ready(ChargeCurve.Series)
        case failed
    }

    private(set) var phase = Phase.loading

    /// The window the web asks for: a minute before the start to a minute after the end; a charge still going (its end
    /// is within 15 minutes of now) runs to now.
    nonisolated static func window(for charge: ActivityCharge, now: Date) -> (since: Date, until: Date) {
        let live = now.timeIntervalSince(charge.endedAt) < 15 * 60
        return (charge.startedAt.addingTimeInterval(-60), (live ? now : charge.endedAt).addingTimeInterval(60))
    }

    func load(source: some ChargeSamplesSource, charge: ActivityCharge, now: Date = .now) async {
        phase = .loading
        let (since, until) = Self.window(for: charge, now: now)
        do {
            let samples = try await source.chargeSamples(since: since, until: until, vehicleID: charge.vehicleId, limit: 2000)
            phase = .ready(ChargeCurve.series(samples, from: charge.startedAt, to: until.addingTimeInterval(-60)))
        } catch {
            // The screen closed while this was in flight: there is nothing to say.
            if Task.isCancelled { return }
            phase = .failed
        }
    }
}
