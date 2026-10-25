import Foundation
import Observation

/// The saved-trip calls, kept apart from `APIClient` so a test can see what is sent.
protocol SavedTripSource: Sendable {
    func savedTrips() async throws -> [SavedTrip]
    func saveTrip(_ body: SavedTripBody) async throws -> SavedTrip
    func updateSavedTrip(id: String, _ body: SavedTripBody) async throws -> SavedTrip
}

extension APIClient: SavedTripSource {}

/// The person's saved trips: the list, saving the trip on screen as a new one or over the one it was opened from. No delete here
/// (that is on the web), and nothing is retried on its own.
@MainActor
@Observable
final class SavedTripsModel {
    enum Phase: Equatable {
        case idle
        case loading
        case loaded
        case failed(String)
    }

    enum SaveState: Equatable {
        case idle
        case saving
        case saved(String)
        case failed(String)
    }

    private(set) var trips: [SavedTrip] = []
    private(set) var phase = Phase.idle
    private(set) var saveState = SaveState.idle
    /// The trip on screen was opened from this one (so "Update" has something to update).
    private(set) var active: SavedTrip?

    func load(source: some SavedTripSource) async {
        guard phase != .loading else { return }
        phase = .loading
        do {
            trips = try await source.savedTrips()
            phase = .loaded
        } catch {
            if Task.isCancelled { phase = .idle; return }
            phase = .failed(SavedTripLogic.failure(error))
        }
    }

    func opened(_ trip: SavedTrip) {
        active = trip
        saveState = .idle
    }

    func forget() {
        active = nil
        saveState = .idle
    }

    /// Saves as a new trip under `name`, or over the active one when `overwrite`.
    func save(source: some SavedTripSource, name: String, inputs: JSONValue, plan: JSONValue?, overwrite: Bool) async {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, saveState != .saving else { return }
        saveState = .saving
        let body = SavedTripBody(name: trimmed, inputs: inputs, plan: plan)
        do {
            let saved: SavedTrip
            if overwrite, let active {
                saved = try await source.updateSavedTrip(id: active.id, body)
            } else {
                saved = try await source.saveTrip(body)
            }
            active = saved
            if let i = trips.firstIndex(where: { $0.id == saved.id }) { trips[i] = saved } else { trips.insert(saved, at: 0) }
            saveState = .saved(saved.name)
        } catch {
            if Task.isCancelled { saveState = .idle; return }
            saveState = .failed(SavedTripLogic.failure(error))
        }
    }
}
