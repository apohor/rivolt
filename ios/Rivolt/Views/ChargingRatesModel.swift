import Foundation
import Observation

/// The two calls behind the rates screen, kept apart from `APIClient` so a test can see what is sent.
protocol ChargingRatesSource: Sendable {
    func chargingRates() async throws -> ChargingRates
    func setChargingRates(pricePerKwh: Double?, currency: String?) async throws -> ChargingRates
}

/// The home rate on this server: read when the screen opens, changed on Save. The saved result shown is the server's own
/// answer, never what was typed. A failure is said once, honestly, and never retried on its own: a refused save leaves
/// the last saved rate as it was shown.
@MainActor
@Observable
final class ChargingRatesModel {
    enum LoadState: Equatable {
        case loading
        case loaded(ChargingRates)
        case failed
    }

    enum SaveError: Equatable {
        /// 400: the server's own words about what it didn't accept.
        case invalid(String)
        /// 403 (and 404 or 405 from a server that has no such route): this server doesn't let the app change it.
        case notAllowed
        /// 429: too many changes in a minute.
        case tooMany
        /// 5xx: the server couldn't save it.
        case serverError
        /// No answer (offline, a timeout): whether it saved is unknown.
        case unknown

        var message: String {
            switch self {
            case .invalid(let m): return "The server didn't accept that: \(m)"
            case .notAllowed: return "Your Rivolt server doesn't let the app change this. Change it on the web."
            case .tooMany: return "That's a lot of changes in a minute. Wait a little, then save again."
            case .serverError: return "The server couldn't save it. Try again in a moment."
            case .unknown: return "No answer from the server, so we can't say whether it saved. Reload to see the saved rate."
            }
        }
    }

    enum SaveState: Equatable {
        case idle
        case saving
        case saved(ChargingRates)
        case failed(SaveError)
    }

    private(set) var loadState = LoadState.loading
    private(set) var saveState = SaveState.idle

    init(load: LoadState = .loading, save: SaveState = .idle) {
        loadState = load
        saveState = save
    }

    /// The rate as the server last said it: what was read, or what the last save answered.
    var saved: ChargingRates? {
        if case .loaded(let r) = loadState { return r }
        return nil
    }

    func load(source: some ChargingRatesSource) async {
        loadState = .loading
        saveState = .idle
        do {
            loadState = .loaded(try await source.chargingRates())
        } catch {
            if Task.isCancelled { return }
            loadState = .failed
        }
    }

    /// Sends only what changed. The answer replaces what is shown as saved.
    func save(source: some ChargingRatesSource, pricePerKwh: Double?, currency: String?) async {
        guard saveState != .saving else { return }
        saveState = .saving
        do {
            let r = try await source.setChargingRates(pricePerKwh: pricePerKwh, currency: currency)
            loadState = .loaded(r)
            saveState = .saved(r)
        } catch {
            if Task.isCancelled { saveState = .idle; return }
            saveState = .failed(Self.saveError(error))
        }
    }

    static func saveError(_ error: Error) -> SaveError {
        guard case APIError.server(let status, let message) = error else { return .unknown }
        switch status {
        case 400: return .invalid(message)
        case 403, 404, 405: return .notAllowed
        case 429: return .tooMany
        case 500...599: return .serverError
        default: return .unknown
        }
    }
}
