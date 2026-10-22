import Foundation
import Observation

/// The one read of a charge's row, kept apart from `APIClient` so a test can count the calls.
protocol ChargeRowSource: Sendable {
    func chargeRow(id: String) async throws -> ChargeRow
}

/// Reads a charge's row once when its screen opens, for the charger map and the session card together (two cards, one
/// request). In memory for the life of the screen; a failure is said once and not retried.
@MainActor
@Observable
final class ChargeRowModel {
    enum Phase: Equatable {
        case loading
        case ready(ChargeRow)
        case failed

        var row: ChargeRow? {
            if case .ready(let r) = self { return r }
            return nil
        }
    }

    private(set) var phase = Phase.loading

    func load(source: some ChargeRowSource, id: String) async {
        phase = .loading
        do {
            phase = .ready(try await source.chargeRow(id: id))
        } catch {
            if Task.isCancelled { return }
            phase = .failed
        }
    }
}
