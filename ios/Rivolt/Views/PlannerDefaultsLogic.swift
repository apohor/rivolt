import Foundation

/// The trip planner defaults form's rules, apart from the view so a test can read them. The same labels and options as the web's
/// "Trip planner defaults" card.
enum PlannerDefaultsForm {
    /// The web offers two drive modes here; a stored one it doesn't offer (SPORT, WINTER, OFF_ROAD_AUTO) is kept until changed.
    struct DriveMode: Equatable, Identifiable {
        let value: String
        let title: String
        var id: String { value }
    }
    static let driveModes = [DriveMode(value: "EVERYDAY", title: "All-Purpose"), DriveMode(value: "DISTANCE", title: "Conserve")]

    enum Adapter: String, CaseIterable, Identifiable {
        case unset, yes, no
        var id: String { rawValue }
        var title: String {
            switch self {
            case .unset: return "Not specified (let planner default)"
            case .yes: return "Yes — I have it"
            case .no: return "No — exclude Superchargers"
            }
        }
        var value: Bool? {
            switch self {
            case .unset: return nil
            case .yes: return true
            case .no: return false
            }
        }
        init(_ v: Bool?) { self = v == nil ? .unset : (v! ? .yes : .no) }
    }

    static let driveModeLabel = "Default drive mode"
    static let adapterLabel = "Tesla NACS adapter"
    static let gasLabel = "Gas $/gal"
    static let mpgLabel = "ICE MPG (comparison)"
    static let gasNote = "Gas $/gal × ICE MPG drives the gas-equivalent chip on route cards. Leave either at 0 to hide it."
    /// The server's bounds: gas 0 to 20 per gallon, MPG 1 to 200.
    static let maxGas = 20.0
    static let minMpg = 1.0
    static let maxMpg = 200.0

    /// The drive mode a screen shows for what is stored: empty (nothing chosen) reads as the first option, as on the web.
    static func shownMode(_ stored: String) -> String { stored.isEmpty ? driveModes[0].value : stored }

    /// Every truck is native NACS by model year only when each is known and 2026 or later; no trucks or an unknown year keeps the question.
    static func askAdapter(modelYears: [Int?]?) -> Bool {
        guard let years = modelYears, !years.isEmpty else { return true }
        return years.contains { ($0 ?? 0) < 2026 }
    }

    struct Draft: Equatable {
        var driveMode: String
        var adapter: Adapter
        var gas: String
        var mpg: String
        /// The map settings; nil where the server does not have them.
        var map: PlannerMapSettings?
    }

    static func draft(_ d: PlannerDefaults) -> Draft {
        Draft(driveMode: shownMode(d.driveMode), adapter: Adapter(d.hasAdapter),
              gas: ChargingRatesForm.fieldText(d.gasPricePerGallon), mpg: ChargingRatesForm.fieldText(d.comparisonMpg), map: d.map)
    }

    enum FieldError: Error, Equatable {
        case gas, mpg
        var message: String {
            switch self {
            case .gas: return "Enter a gas price like 4.00 (0 to \(Int(PlannerDefaultsForm.maxGas)))."
            case .mpg: return "Enter miles per gallon like 20 (\(Int(PlannerDefaultsForm.minMpg)) to \(Int(PlannerDefaultsForm.maxMpg)), or empty to hide the chip)."
            }
        }
    }

    /// Empty is 0 (hide the chip). Digits and one decimal mark (comma or point), no grouping.
    static func number(_ text: String, max: Double) -> Double? {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: ",", with: ".")
        if t.isEmpty { return 0 }
        guard t.allSatisfy({ ($0 >= "0" && $0 <= "9") || $0 == "." }), t.filter({ $0 == "." }).count <= 1,
              t.contains(where: { $0 >= "0" && $0 <= "9" }), let v = Double(t), v.isFinite, v >= 0, v <= max else { return nil }
        return v
    }

    /// MPG is 0 (hides the chip) or 1 to 200 on the server.
    static func errors(_ d: Draft) -> [FieldError] {
        var out: [FieldError] = []
        if number(d.gas, max: maxGas) == nil { out.append(.gas) }
        if let m = number(d.mpg, max: maxMpg), m == 0 || m >= minMpg {} else { out.append(.mpg) }
        return out
    }

    /// The planner's own settings, only when they differ from what is saved. The mode and adapter go together (the server replaces both), and where the
    /// server has the map settings every one goes too: a save is the whole set, so none is ever left out and cleared.
    static func prefsPatch(_ d: Draft, saved: PlannerDefaults) -> PlannerPrefs? {
        let mode = d.driveMode
        let adapter = d.adapter.value
        if mode == shownMode(saved.driveMode) && adapter == saved.hasAdapter && d.map == saved.map { return nil }
        var p = PlannerPrefs(driveMode: mode, hasAdapter: adapter)
        if let m = d.map {
            p.corridor = m.corridor
            p.startExcludeMi = m.startExcludeMi
            p.minFastKw = m.minFastKw
            // In the server's own order, so the same choice is always the same list.
            p.layersOn = PlannerSettingsCopy.layers.map(\.kind).filter { m.layersOn.contains($0) }
            p.preferredOnly = m.preferredOnly
            p.weatherOn = m.weatherOn
            p.radarOn = m.radarOn
        }
        return p
    }

    /// The gas inputs, only the one(s) that changed.
    static func costsPatch(_ d: Draft, saved: PlannerDefaults) -> ChargingRatesPatch? {
        var p = ChargingRatesPatch()
        if let g = number(d.gas, max: maxGas), abs(g - saved.gasPricePerGallon) > 1e-9 { p.gasPricePerGallon = g }
        if let m = number(d.mpg, max: maxMpg), abs(m - saved.comparisonMpg) > 1e-9 { p.comparisonMpg = m }
        return p == ChargingRatesPatch() ? nil : p
    }

    static func changed(_ d: Draft, saved: PlannerDefaults) -> Bool { prefsPatch(d, saved: saved) != nil || costsPatch(d, saved: saved) != nil }
}


/// The words of the Trip planner page: its groups, each setting and the one line under it, and the choices. One plain sentence under every setting; a
/// setting that is off by default is worded as an invitation, never as if it were on.
enum PlannerSettingsCopy {
    static let chargersGroup = "Chargers on the map"
    static let planningGroup = "Planning"
    static let mapGroup = "Map"
    static let costsGroup = "Costs"

    struct Choice<Value: Hashable>: Identifiable {
        let value: Value
        let title: String
        var id: Value { value }
    }

    static let corridorLabel = "Distance from your route"
    static let corridors = [Choice(value: "close", title: "Close"), Choice(value: "normal", title: "Normal"), Choice(value: "wide", title: "Wide")]
    /// Fast chargers: 2, 5 or 10 mi (3, 8 or 16 km).
    static func corridorNote(metric: Bool) -> String {
        "How far off the route a charger can be and still show. Fast chargers: about \(metric ? "3 km, 8 km or 16 km" : "2 mi, 5 mi or 10 mi")."
    }

    static let startLabel = "Hide chargers near the start"
    static func startChoices(metric: Bool) -> [Choice<Int>] {
        [Choice(value: 0, title: "Off"), Choice(value: 25, title: metric ? "First 40 km" : "First 25 mi"), Choice(value: 50, title: metric ? "First 80 km" : "First 50 mi")]
    }
    static let startNote = "Turn on to hide chargers in the first miles of the first day, if you usually start charged. Rivian's planned stops always show."

    static let minFastLabel = "Smallest fast charger to show"
    static let minFast = [Choice(value: 0, title: "Any"), Choice(value: 50, title: "50 kW"), Choice(value: 150, title: "150 kW"), Choice(value: 250, title: "250 kW")]
    static let minFastNote = "Hides fast chargers rated below this. A charger with no listed power stays."

    static let layersLabel = "Pins on when a route opens"
    /// The server's layer kinds, with the charger-type words.
    static let layers: [(kind: String, title: String)] = MapLayer.allCases.map { ($0.kind, $0.title) }
    static let layersNote = "You can still switch them on and off on the map."

    static let preferredLabel = "Only my preferred networks"
    static let preferredNote = "Fast pins only from the networks you prefer. Choose them under Charging networks in Settings."

    static let weatherLabel = "Weather on the route"
    static let weatherNote = "The temperature and wind on each leg, on by default."
    static let radarLabel = "Radar"
    static let radarNote = "Live NOAA radar under the route. Shown only where this server has radar."
}
