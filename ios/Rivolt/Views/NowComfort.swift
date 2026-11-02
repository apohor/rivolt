import Foundation

/// What the truck says about its cabin right now, for the Now card's one comfort line. Read as reported, never worked
/// out: a value the truck doesn't give, or gives as "undefined", "unknown", "off", "disabled" or "faulty", is left out.
/// Plain text on purpose: pet mode in particular is never coloured or marked as an alert, because a late frame must not
/// read as an alarm about an animal, and the line says nothing about the animal's safety or a temperature limit.
struct NowComfort: Equatable {
    enum Preconditioning: Equatable { case under, done }

    /// Celsius; nil when the truck gave none (it sends 0 for "no reading").
    let cabinTempC: Double?
    let preconditioning: Preconditioning?
    let petMode: Bool
    let climateHold: Bool

    init?(cabinTempC: Double?, preconditioning: String?, petMode: String?, cabinHold: String?) {
        let temp = cabinTempC.flatMap { $0 != 0 && $0 > -50 && $0 < 80 ? $0 : nil }
        let precon: Preconditioning?
        switch Self.clean(preconditioning) {
        case "preconditioning": precon = .under
        case "preconditioned": precon = .done
        default: precon = nil
        }
        let pet = Self.isOn(petMode), hold = Self.isOn(cabinHold)
        guard temp != nil || precon != nil || pet || hold else { return nil }
        self.cabinTempC = temp
        self.preconditioning = precon
        self.petMode = pet
        self.climateHold = hold
    }

    private static func clean(_ s: String?) -> String { (s ?? "").lowercased().trimmingCharacters(in: .whitespaces) }

    /// The same words the web's live panel reads as on.
    private static func isOn(_ s: String?) -> Bool { ["true", "on", "active", "1"].contains(clean(s)) }

    /// A temperature in whole degrees, in the account's temperature unit.
    static func wholeDegrees(celsius: Double, spoken: Bool = false) -> String {
        let shown = DisplayUnits.celsius ? Measurement(value: celsius, unit: UnitTemperature.celsius) : Measurement(value: celsius, unit: UnitTemperature.celsius).converted(to: .fahrenheit)
        return shown.formatted(.measurement(width: spoken ? .wide : .abbreviated, usage: .asProvided, numberFormatStyle: .number.precision(.fractionLength(0))))
    }

    /// The cabin temperature for its own row beside Outside; nil when the truck gave none.
    var cabinValue: String? { cabinTempC.map { Self.wholeDegrees(celsius: $0) } }

    /// The parts, in order: cabin temperature (left out when it has its own row), preconditioning, pet mode, climate hold.
    func parts(spoken: Bool = false, includeCabin: Bool = true) -> [String] {
        var out: [String] = []
        if includeCabin, let c = cabinTempC {
            out.append("Cabin \(Self.wholeDegrees(celsius: c, spoken: spoken))")
        }
        switch preconditioning {
        case .under?: out.append("Preconditioning")
        case .done?: out.append("Preconditioned")
        case nil: break
        }
        if petMode { out.append("Pet mode on") }
        if climateHold { out.append("Climate hold") }
        return out
    }

    var text: String { parts().joined(separator: " · ") }
    /// The one line under the rows: everything but the cabin temperature, which is a row. Empty when there is nothing else.
    var lineText: String { parts(includeCabin: false).joined(separator: " · ") }
    var spokenText: String { parts(spoken: true).joined(separator: ", ") }
}
