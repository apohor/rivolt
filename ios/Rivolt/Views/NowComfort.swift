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

/// The truck's software update state for the Truck tab's Vehicle card and, while it installs, the Now card. Read from the live feed's
/// own words (a fixed list; idle, empty or any other word means none and no claim; an R2 sends none), in plain text: never an
/// instruction, never an estimate.
struct NowUpdate: Equatable {
    enum Phase: Equatable { case downloading, ready, installing }

    let phase: Phase
    let version: String?
    /// Whole percent; nil until the progress scale is confirmed from real frames (see `progressMax`), and for "ready".
    let pct: Int?

    /// The maximum of `otaInstallProgress` (100 for a percent, 1 for a fraction). Nil until confirmed: with it nil no number is shown,
    /// only the state, never "4000%" or "0.4%".
    static let progressMax: Double? = nil

    private static let words: [String: Phase] = [
        "downloading": .downloading, "download": .downloading, "download_in_progress": .downloading,
        "ready_to_install": .ready, "ready_for_install": .ready,
        "installing": .installing, "install": .installing, "install_in_progress": .installing,
    ]

    init?(status: String?, progress: Double?, current: String?, available: String?) {
        let key = (status ?? "").trimmingCharacters(in: .whitespaces).lowercased().split(whereSeparator: { $0 == " " || $0 == "-" || $0 == "_" }).joined(separator: "_")
        guard let phase = Self.words[key] else { return nil }
        self.phase = phase
        if let max = Self.progressMax, phase != .ready, let p = progress, p.isFinite, p > 0, p <= max { pct = Int((p / max * 100).rounded()) } else { pct = nil }
        let cur = (current ?? "").trimmingCharacters(in: .whitespaces)
        let avail = (available ?? "").trimmingCharacters(in: .whitespaces)
        let none = avail.isEmpty || avail.allSatisfy { $0 == "0" || $0 == "." }
        version = !cur.isEmpty && !none && avail != cur ? avail : nil
    }

    init?(_ s: VehicleState) {
        self.init(status: s.otaStatus, progress: s.otaInstallProgress, current: s.otaCurrentVersion, available: s.otaAvailableVersion)
    }

    /// "2026.38 · Downloading 40%", "Ready to install"; on Now, "Installing software 2026.38 · 40%".
    func line(onNow: Bool = false) -> String {
        if onNow { return "Installing software" + (version.map { " \($0)" } ?? "") + (pct.map { " · \($0)%" } ?? "") }
        let num = pct.map { " \($0)%" } ?? ""
        let word: String
        switch phase {
        case .downloading: word = "Downloading" + num
        case .ready: word = "Ready to install"
        case .installing: word = "Installing" + num
        }
        return version.map { "\($0) · \(word)" } ?? word
    }
}
