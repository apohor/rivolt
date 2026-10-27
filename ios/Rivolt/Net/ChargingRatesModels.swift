import Foundation

/// The home electricity rate from `GET /api/settings/charging` (and the answer to the app's one settings write): the
/// price per kWh and its currency. The same settings also hold the trip planner's gas price and MPG, which the planner-defaults
/// screen reads and sends. Held in memory while the screen is open.
struct ChargingRates: Decodable, Equatable {
    /// 0 means "not set": charges Rivian reports as free are not priced.
    let homePricePerKwh: Double
    let homeCurrency: String
    /// The trip planner's gas-equivalent inputs; 0 or absent hides the chip.
    var gasPricePerGallon: Double? = nil
    var comparisonMpg: Double? = nil
}

/// The body of `PATCH /api/settings/charging`: only the fields the person changed (the server merges what it is sent
/// and refuses an empty body or a field it doesn't know). Keys go out as `home_price_per_kwh` and `home_currency`.
struct ChargingRatesPatch: Encodable, Equatable {
    var homePricePerKwh: Double?
    var homeCurrency: String?
    var gasPricePerGallon: Double?
    var comparisonMpg: Double?
}
