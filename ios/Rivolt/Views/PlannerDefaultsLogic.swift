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
    static let maxGas = 100.0
    static let maxMpg = 500.0

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
    }

    static func draft(_ d: PlannerDefaults) -> Draft {
        Draft(driveMode: shownMode(d.driveMode), adapter: Adapter(d.hasAdapter),
              gas: ChargingRatesForm.fieldText(d.gasPricePerGallon), mpg: ChargingRatesForm.fieldText(d.comparisonMpg))
    }

    enum FieldError: Error, Equatable {
        case gas, mpg
        var message: String {
            switch self {
            case .gas: return "Enter a gas price like 4.00 (0 to \(Int(PlannerDefaultsForm.maxGas)))."
            case .mpg: return "Enter miles per gallon like 20 (0 to \(Int(PlannerDefaultsForm.maxMpg)))."
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

    static func errors(_ d: Draft) -> [FieldError] {
        var out: [FieldError] = []
        if number(d.gas, max: maxGas) == nil { out.append(.gas) }
        if number(d.mpg, max: maxMpg) == nil { out.append(.mpg) }
        return out
    }

    /// The planner's own settings, only when they differ from what is saved. The mode and adapter go together (the server replaces both).
    static func prefsPatch(_ d: Draft, saved: PlannerDefaults) -> PlannerPrefs? {
        let mode = d.driveMode
        let adapter = d.adapter.value
        if mode == shownMode(saved.driveMode) && adapter == saved.hasAdapter { return nil }
        return PlannerPrefs(driveMode: mode, hasAdapter: adapter)
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
