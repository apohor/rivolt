import Foundation
import Observation

/// One settings card's state: read when the screen opens, changed on Save, and the answer shown is the server's own, never what was
/// typed. A failure is said once, in plain words, and never retried on its own; a refused save leaves the last saved value as it
/// was shown. The same words as the home-rate screen (`ChargingRatesModel.SaveError`).
@MainActor
@Observable
final class SettingsCardModel<Value: Equatable> {
    enum LoadState: Equatable {
        case loading
        case loaded(Value)
        case failed
    }

    enum SaveState: Equatable {
        case idle
        case saving
        case saved(Value)
        case failed(ChargingRatesModel.SaveError)
    }

    private(set) var loadState = LoadState.loading
    private(set) var saveState = SaveState.idle

    init(load: LoadState = .loading, save: SaveState = .idle) {
        loadState = load
        saveState = save
    }

    var saved: Value? {
        if case .loaded(let v) = loadState { return v }
        return nil
    }

    func load(_ read: () async throws -> Value) async {
        loadState = .loading
        saveState = .idle
        do {
            loadState = .loaded(try await read())
        } catch {
            if Task.isCancelled { return }
            loadState = .failed
        }
    }

    /// Runs one write. The answer replaces what is shown as saved.
    func save(_ write: () async throws -> Value) async {
        guard saveState != .saving else { return }
        saveState = .saving
        do {
            let v = try await write()
            loadState = .loaded(v)
            saveState = .saved(v)
        } catch {
            if Task.isCancelled { saveState = .idle; return }
            saveState = .failed(ChargingRatesModel.saveError(error))
        }
    }

    /// Clears a shown "Saved" or error once the person edits again.
    func clearStatus() {
        if saveState != .saving { saveState = .idle }
    }
}

/// The trip planner defaults as one screen holds them: the planner's own two settings plus the gas inputs that live with the
/// charging settings, and whether any truck on the account needs the NACS adapter question.
struct PlannerDefaults: Equatable, Hashable {
    var driveMode: String
    var hasAdapter: Bool?
    var gasPricePerGallon: Double
    var comparisonMpg: Double
    var askAdapter: Bool
    /// The chargers-on-the-map and map settings; nil when the server does not have them (then those groups are not shown).
    var map: PlannerMapSettings? = nil
}

/// The Trip planner page's map settings, with the server's defaults (corridor normal, nothing hidden near the start, any fast power, Fast pins on,
/// weather on, radar off).
struct PlannerMapSettings: Equatable, Hashable {
    var corridor = "normal"
    var startExcludeMi = 0
    var minFastKw = 0
    var layersOn: Set<String> = ["dcfc"]
    var preferredOnly = false
    var weatherOn = true
    var radarOn = false

    init() {}

    /// From what the server sent; nil when it sent none of them (an older server).
    init?(_ p: PlannerPrefs) {
        guard p.corridor != nil || p.startExcludeMi != nil || p.minFastKw != nil || p.layersOn != nil || p.preferredOnly != nil || p.weatherOn != nil || p.radarOn != nil else { return nil }
        corridor = p.corridor ?? "normal"
        startExcludeMi = p.startExcludeMi ?? 0
        minFastKw = p.minFastKw ?? 0
        layersOn = Set(p.layersOn ?? ["dcfc"])
        preferredOnly = p.preferredOnly ?? false
        weatherOn = p.weatherOn ?? true
        radarOn = p.radarOn ?? false
    }
}

/// The calls behind the three screens, kept apart from `APIClient` so a test can see what is sent.
protocol ChargingSettingsSource: Sendable {
    func offpeak() async throws -> OffpeakSchedule
    func setOffpeak(_ body: OffpeakPut) async throws -> OffpeakSchedule
    func setFlatRate(_ on: Bool) async throws -> FlatRateBody
    func chargingNetworks() async throws -> [ChargingNetwork]
    func setChargingNetworks(_ rows: [ChargingNetwork]) async throws -> [ChargingNetwork]
    func resetChargingNetworks() async throws -> [ChargingNetwork]
    func membershipTerms() async throws -> [MembershipTerm]
    func plannerPrefs() async throws -> PlannerPrefs
    func setPlannerPrefs(_ prefs: PlannerPrefs) async throws -> PlannerPrefs
    func chargingRates() async throws -> ChargingRates
    func setChargingRates(_ patch: ChargingRatesPatch) async throws -> ChargingRates
    func vehicles() async throws -> [Vehicle]
}

extension ChargingSettingsSource {
    /// The planner screen's read: the prefs, the gas inputs and (when the account's trucks are known) whether to ask about the adapter.
    func plannerDefaults() async throws -> PlannerDefaults {
        async let prefs = plannerPrefs()
        async let costs = chargingRates()
        let (p, c) = try await (prefs, costs)
        // The adapter question is hidden only when every truck is native NACS by model year (2026 and later), as on the web;
        // a failed or empty vehicle list keeps it shown.
        let ask = PlannerDefaultsForm.askAdapter(modelYears: (try? await vehicles())?.map(\.modelYear))
        return PlannerDefaults(driveMode: p.driveMode, hasAdapter: p.hasAdapter, gasPricePerGallon: c.gasPricePerGallon ?? 0,
                               comparisonMpg: c.comparisonMpg ?? 0, askAdapter: ask, map: PlannerMapSettings(p))
    }

    /// Sends only what changed: the planner's own settings, and the gas inputs on the charging settings. The answer is the read again.
    func savePlannerDefaults(_ draft: PlannerDefaultsForm.Draft, saved: PlannerDefaults) async throws -> PlannerDefaults {
        if let prefs = PlannerDefaultsForm.prefsPatch(draft, saved: saved) { _ = try await setPlannerPrefs(prefs) }
        if let patch = PlannerDefaultsForm.costsPatch(draft, saved: saved) { _ = try await setChargingRates(patch) }
        return try await plannerDefaults()
    }
}
