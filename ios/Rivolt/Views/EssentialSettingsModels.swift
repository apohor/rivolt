import Foundation
import Observation

/// The calls behind the home-location and vehicle screens, kept apart from `APIClient` so a test can see what is sent.
protocol EssentialSettingsSource: Sendable {
    func homeLocation() async throws -> HomeLocation
    func setHomeLocation(_ body: HomeLocationBody) async throws -> HomeLocation
    func restoreHomeLocation() async throws -> HomeLocation
    func searchHomeAddress(_ q: String) async throws -> [PlaceMatch]
    func vehicleProfile(id: String) async throws -> VehicleProfile
    func saveVehicleProfile(id: String, _ profile: VehicleProfile) async throws -> VehicleProfile
    func truckPosition(id: String) async throws -> TruckPosition
}

extension APIClient: EssentialSettingsSource {}

enum EssentialSettingsWords {
    /// What to tell the person when one of these calls failed; never the server's own text.
    static func failure(_ error: Error, restoring: Bool = false) -> String {
        if case APIError.server(let status, _) = error {
            switch status {
            case 429: return "That's a lot of changes in a minute. Wait a little, then try again."
            case 404: return restoring ? "There's no earlier home to go back to." : "This server can't do that."
            case 403: return "This server doesn't let the app change that. Use the web."
            case 400: return "That place wasn't accepted. Try another."
            default: break
            }
        }
        return UserFacingError.words(error)
    }
}

/// The saved home, read when the screen opens, and its changes: set from an address match or the truck's position, restore,
/// clear. The home shown is always the server's own answer. The app never reads the phone's location.
@MainActor
@Observable
final class HomeLocationModel {
    enum LoadState: Equatable { case loading, loaded(HomeLocation), failed }

    private(set) var loadState = LoadState.loading
    private(set) var busy = false
    private(set) var results: [PlaceMatch] = []
    /// The last thing said to the person: a success or a plain failure.
    private(set) var message: String?
    private(set) var messageIsError = false

    var home: HomeLocation? { if case .loaded(let h) = loadState { return h } else { return nil } }

    func load(source: some EssentialSettingsSource) async {
        loadState = .loading
        do { loadState = .loaded(try await source.homeLocation()) } catch {
            if Task.isCancelled { return }
            loadState = .failed
        }
    }

    func search(source: some EssentialSettingsSource, query: String) async {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard q.count >= 3, !busy else { return }
        busy = true
        defer { busy = false }
        message = nil
        do {
            results = try await source.searchHomeAddress(q)
            if results.isEmpty { say("No match for that address. Try the street, number and city.", error: false) }
        } catch {
            if Task.isCancelled { return }
            results = []
            say(EssentialSettingsWords.failure(error), error: true)
        }
    }

    func setHome(source: some EssentialSettingsSource, place: PlaceMatch) async {
        await write(source: source, body: HomeLocationBody(set: true, latitude: place.latitude, longitude: place.longitude, label: "Home", address: place.line, source: "address"),
                    done: "Home set.")
    }

    /// From the truck's last reported position; false when it has none (the truck reports a position of 0, 0 without a fix).
    func setHomeToTruck(source: some EssentialSettingsSource, vehicleID: String, now: Date = .now) async {
        guard !busy else { return }
        busy = true
        message = nil
        let position: TruckPosition
        do { position = try await source.truckPosition(id: vehicleID) } catch {
            busy = false
            say(EssentialSettingsWords.failure(error), error: true)
            return
        }
        busy = false
        guard let c = position.coordinate else {
            say("The truck hasn't reported where it is. Try again once it has been driven or woken.", error: false)
            return
        }
        let done = position.staleFix(now: now).map { "Home set to where the truck was last seen, \($0.formatted(date: .abbreviated, time: .shortened))." } ?? "Home set to where the truck is."
        await write(source: source, body: HomeLocationBody(set: true, latitude: c.latitude, longitude: c.longitude, label: "Home", address: nil, source: "coordinates"),
                    done: done)
    }

    func clear(source: some EssentialSettingsSource) async {
        await write(source: source, body: .clear, done: "Home cleared.")
    }

    func restore(source: some EssentialSettingsSource) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        message = nil
        do {
            loadState = .loaded(try await source.restoreHomeLocation())
            results = []
            say("Earlier home restored.", error: false)
        } catch {
            if Task.isCancelled { return }
            say(EssentialSettingsWords.failure(error, restoring: true), error: true)
        }
    }

    private func write(source: some EssentialSettingsSource, body: HomeLocationBody, done: String) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        message = nil
        do {
            loadState = .loaded(try await source.setHomeLocation(body))
            results = []
            say(done, error: false)
        } catch {
            if Task.isCancelled { return }
            say(EssentialSettingsWords.failure(error), error: true)
        }
    }

    private func say(_ text: String, error: Bool) {
        message = text
        messageIsError = error
    }
}

/// The truck's profile (tires, wheels, accessories, extra load, trailer, placard, charge port): read, edited as a draft and saved
/// in one write. The saved profile shown is the server's own answer.
@MainActor
@Observable
final class VehicleProfileModel {
    enum LoadState: Equatable { case loading, loaded(VehicleProfile), failed }
    enum SaveState: Equatable { case idle, saving, saved, failed(String) }

    private(set) var loadState = LoadState.loading
    private(set) var saveState = SaveState.idle

    var saved: VehicleProfile? { if case .loaded(let p) = loadState { return p } else { return nil } }

    func load(source: some EssentialSettingsSource, vehicleID: String) async {
        loadState = .loading
        saveState = .idle
        do { loadState = .loaded(try await source.vehicleProfile(id: vehicleID)) } catch {
            if Task.isCancelled { return }
            loadState = .failed
        }
    }

    func save(source: some EssentialSettingsSource, vehicleID: String, _ profile: VehicleProfile) async {
        guard saveState != .saving else { return }
        saveState = .saving
        do {
            loadState = .loaded(try await source.saveVehicleProfile(id: vehicleID, profile))
            saveState = .saved
        } catch {
            if Task.isCancelled { saveState = .idle; return }
            saveState = .failed(EssentialSettingsWords.failure(error))
        }
    }
}

/// The choices the profile screen offers (the web's), with their words.
enum VehicleProfileChoices {
    static let tires: [(value: String, label: String)] = [("", "Not set"), ("all_season", "All-season"), ("all_terrain", "All-terrain"), ("winter", "Winter"), ("summer", "Summer")]
    static let wheels: [Int] = [0, 20, 21, 22]
    static let accessories: [(value: String, label: String)] = [
        ("roof_rack", "Roof rack"), ("cargo_box", "Cargo box"), ("bike_rack", "Bike rack"), ("rooftop_tent", "Rooftop tent"),
        ("ski_box", "Ski or cargo box (winter)"), ("running_boards", "Running boards"),
    ]
    /// Automatic (from the model year), the truck's own port, or CCS with a Tesla adapter.
    static let chargePort: [(value: String, label: String)] = [("", "Automatic"), ("native", "Native NACS"), ("ccs", "CCS (needs an adapter for Tesla chargers)")]

    static func chargePortValue(_ nacs: Bool?) -> String { nacs == true ? "native" : nacs == false ? "ccs" : "" }
    static func nacs(forChargePort v: String) -> Bool? { v == "native" ? true : v == "ccs" ? false : nil }
}
