import Foundation

/// Numbers as the web writes them. The web's `toFixed` and `Intl.NumberFormat` round a half away from zero ("2.25" gives
/// "2.3", "0.125" gives "0.13") and `toFixed` never groups digits ("1120.2"); Foundation's formatter rounds halves to even
/// and groups by locale, so a figure on the phone could differ from the same figure on the web in the last digit.
/// Every figure the app shows goes through here.
enum NumberText {
    /// `digits` decimal places, a half rounded away from zero, no thousands separators.
    static func fixed(_ v: Double, _ digits: Int) -> String {
        v.formatted(.number.precision(.fractionLength(digits)).grouping(.never).rounded(rule: .toNearestOrAwayFromZero))
    }

    /// An amount in a currency code (two decimals, a half rounded away from zero); plain digits when the code isn't a
    /// usable one.
    static func money(_ v: Double, _ currency: String, digits: Int = 2) -> String {
        guard currency.count == 3 else { return fixed(v, digits) }
        return v.formatted(.currency(code: currency.uppercased()).precision(.fractionLength(digits)).rounded(rule: .toNearestOrAwayFromZero))
    }
}
