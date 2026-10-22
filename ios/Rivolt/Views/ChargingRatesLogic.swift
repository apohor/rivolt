import Foundation

/// The rates form's rules, apart from the view so a test can read them: what text is a rate, what changed, and what
/// to say about each way a save can fail.
enum ChargingRatesForm {
    /// The currencies the server accepts for the home rate.
    static let currencies = ["USD", "CAD", "EUR", "GBP", "AUD", "NZD", "CHF", "SEK", "NOK", "DKK"]
    /// The server refuses a rate above this per kWh.
    static let maxRate = 5.0
    static let maxDecimals = 4

    enum RateError: Equatable {
        case notANumber, tooManyDecimals, outOfRange

        var message: String {
            switch self {
            case .notANumber: return "Enter a rate like 0.15."
            case .tooManyDecimals: return "Use at most \(ChargingRatesForm.maxDecimals) decimal places."
            case .outOfRange: return "Enter a rate from 0 to \(NumberText.fixed(ChargingRatesForm.maxRate, 0)) per kWh."
            }
        }
    }

    /// What the person typed as a rate. Empty is 0 (not set), as on the web. A comma or a point is the decimal mark,
    /// whichever the keyboard offers; digits only, no grouping, no exponent.
    static func parse(_ text: String) -> Result<Double, RateError> {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: ",", with: ".")
        if t.isEmpty { return .success(0) }
        guard t.allSatisfy({ ($0 >= "0" && $0 <= "9") || $0 == "." }),
              t.filter({ $0 == "." }).count <= 1, t.contains(where: { $0 >= "0" && $0 <= "9" }) else { return .failure(.notANumber) }
        let decimals = t.split(separator: ".", omittingEmptySubsequences: false).dropFirst().first?.count ?? 0
        if decimals > maxDecimals { return .failure(.tooManyDecimals) }
        guard let v = Double(t), v.isFinite else { return .failure(.notANumber) }
        if v < 0 || v > maxRate { return .failure(.outOfRange) }
        return .success(v)
    }

    /// A rate as it goes in the field: no grouping, the keyboard's own decimal mark, nothing for "not set".
    static func fieldText(_ rate: Double, locale: Locale = .current) -> String {
        guard rate > 0 else { return "" }
        var s = NumberText.fixed(rate, maxDecimals)
        if s.contains(".") {
            while s.hasSuffix("0") { s.removeLast() }
            if s.hasSuffix(".") { s.removeLast() }
        }
        if let mark = locale.decimalSeparator, mark != "." { s = s.replacingOccurrences(of: ".", with: mark) }
        return s
    }

    /// Only what differs from what is saved: the patch to send, nil when nothing changed.
    static func patch(rate: Double, currency: String, saved: ChargingRates) -> ChargingRatesPatch? {
        var p = ChargingRatesPatch()
        if abs(rate - saved.homePricePerKwh) > 1e-9 { p.homePricePerKwh = rate }
        if currency.uppercased() != saved.homeCurrency.uppercased() { p.homeCurrency = currency.uppercased() }
        return p == ChargingRatesPatch() ? nil : p
    }

    /// The rate as the server has it, in one line.
    static func summary(_ r: ChargingRates) -> String {
        guard r.homePricePerKwh > 0 else { return "No home rate set" }
        return "\(NumberText.money(r.homePricePerKwh, r.homeCurrency, digits: 3))/kWh"
    }

    /// The summary as VoiceOver reads it.
    static func spokenSummary(_ r: ChargingRates) -> String {
        guard r.homePricePerKwh > 0 else { return "No home rate set" }
        return "\(NumberText.money(r.homePricePerKwh, r.homeCurrency, digits: 3)) per kilowatt hour"
    }

    static let explanation = "Applied to charges Rivian reports as free: home charging, and Level 2 away from Rivian's own chargers. Leave it empty or at 0 to turn it off."
    static let afterSave = "Money and Activity show the new prices when you refresh them."
}
