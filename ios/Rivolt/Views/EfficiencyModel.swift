import Foundation
import Observation

/// The read behind Activity → Efficiency, kept apart from `APIClient` so tests can fake it.
protocol EfficiencySource: Sendable {
    func efficiency(vehicleID: String, units: String, since: Date, until: Date) async throws -> EfficiencyInsights?
}

extension APIClient: EfficiencySource {}

/// One truck's efficiency for the Activity month, read when the Efficiency view opens and kept in memory for 15
/// minutes per truck, month and units (as ActivityModel: the server reads every drive's samples for it). Dropped on
/// sign-out and on another server (AppState).
@MainActor
@Observable
final class EfficiencyModel {
    static let lifetime: TimeInterval = 15 * 60

    enum Shown: Equatable {
        case loading
        /// The server has no efficiency endpoint (404).
        case unavailable
        case insights(EfficiencyInsights)
        case failed
    }

    private(set) var shown = Shown.loading

    private struct Entry {
        let value: EfficiencyInsights?
        let at: Date
    }
    @ObservationIgnored private var cache: [String: Entry] = [:]
    @ObservationIgnored private var key: String?

    func load(source: some EfficiencySource, vehicleID: String, month: PeriodMonth, metric: Bool, force: Bool = false,
              now: Date = .now, calendar: Calendar = .current) async {
        let units = metric ? "metric" : "imperial"
        let k = "\(vehicleID)|\(month.key)|\(units)"
        if !force, let hit = cache[k], now.timeIntervalSince(hit.at) < Self.lifetime {
            key = k
            shown = hit.value.map(Shown.insights) ?? .unavailable
            return
        }
        // Another truck, month or units: never the last one's figures meanwhile.
        if k != key { shown = .loading }
        key = k
        let (since, until) = month.range(calendar: calendar)
        do {
            let v = try await source.efficiency(vehicleID: vehicleID, units: units, since: since, until: until)
            guard key == k else { return }
            cache[k] = Entry(value: v, at: now)
            shown = v.map(Shown.insights) ?? .unavailable
        } catch {
            guard key == k else { return }
            if case .loading = shown { shown = .failed }
        }
    }

    /// Forgets everything (sign-out, another server).
    func clear() {
        cache = [:]
        key = nil
        shown = .loading
    }
}
