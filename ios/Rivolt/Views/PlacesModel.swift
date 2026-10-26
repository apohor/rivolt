import Foundation
import Observation

/// The one read behind the "Charging locations" card. Kept apart from `APIClient` so tests can fake it.
protocol PlacesSource: Sendable {
    func chargeSplit(since: Date, until: Date, vehicleID: String) async throws -> ChargeSplit
}

extension APIClient: PlacesSource {}

/// Each bucket's share of the energy as whole percents that add up to exactly 100 (largest remainder), so the percent
/// next to a kWh figure is the kWh share. The same as the web's `shareByEnergy`.
enum PlacesMath {
    static func shareByEnergy(_ kwh: [Double]) -> [Int] {
        let total = kwh.reduce(0) { $0 + max(0, $1) }
        guard total > 0 else { return kwh.map { _ in 0 } }
        let exact = kwh.map { max(0, $0) / total * 100 }
        var floors = exact.map { Int($0.rounded(.down)) }
        var left = 100 - floors.reduce(0, +)
        for (i, _) in exact.enumerated().sorted(by: { ($0.element - $0.element.rounded(.down)) > ($1.element - $1.element.rounded(.down)) }) {
            if left <= 0 { break }
            floors[i] += 1
            left -= 1
        }
        return floors
    }
}

/// Loads the split and keeps each answer in memory for 15 minutes per truck and period: the server groups up to 5,000
/// charges for every call and rate-limits it. Pull-to-refresh forces a read. A failed read (including a 429) keeps what
/// is on screen and is not retried until the next pull or the next period or truck change.
@MainActor
@Observable
final class PlacesModel {
    static let lifetime: TimeInterval = 15 * 60

    private(set) var split: ChargeSplit?

    private struct Entry {
        let split: ChargeSplit
        let fetchedAt: Date
    }
    @ObservationIgnored private var cache: [String: Entry] = [:]

    init(split: ChargeSplit? = nil) { self.split = split }

    func load(source: some PlacesSource, vehicleID: String, month: PeriodMonth, force: Bool = false,
              now: Date = .now, calendar: Calendar = .current) async {
        let key = "\(vehicleID)|\(month.key)"
        if !force, let hit = cache[key], now.timeIntervalSince(hit.fetchedAt) < Self.lifetime {
            split = hit.split
            return
        }
        let (since, until) = month.range(calendar: calendar)
        do {
            let fresh = try await source.chargeSplit(since: since, until: until, vehicleID: vehicleID)
            cache[key] = Entry(split: fresh, fetchedAt: now)
            split = fresh
        } catch {
            // Keep what is shown for the same truck and period; anything else would be another period's figures.
            split = cache[key]?.split
        }
    }
}
