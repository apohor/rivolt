import Foundation

/// `POST /api/trips/saved/{id}/receipt` (design_preview key `trip_receipt`): the data for the receipt image of a saved trip. Every place is an
/// area ("Pasadena area") or a public charger's own name; figures near the start, the destination and added stops are rounded (`rounded`); `sketch`
/// is the route cut at both ends, at every added stop and at home, and is sent only when asked for. The app draws the image; nothing is uploaded.
struct TripReceipt: Decodable, Equatable {
    struct Row: Decodable, Equatable {
        /// "drive", "charge" or "stop".
        var kind: String
        var from: String?
        var to: String?
        var name: String?
        var distM: Double?
        var durS: Double?
        var fromSoc: Double
        var toSoc: Double
        var maxKw: Double?
        var rounded: Bool?
    }

    struct Totals: Decodable, Equatable {
        var distM: Double
        var driveS: Double
        var chargeS: Double
        var stops: Int
        var arriveAt: Date?
        var arrivalSoc: Double
    }

    var departAt: Date?
    /// The destination's zone (IANA), for the arrival clock.
    var tz: String?
    var startSoc: Double
    var start: String
    var end: String
    var rows: [Row]
    var totals: Totals
    var sketch: [String]?
}

/// The body of the receipt call.
struct TripReceiptRequest: Encodable, Equatable {
    var sketch: Bool
    var metric: Bool
}
