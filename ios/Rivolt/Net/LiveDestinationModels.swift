import Foundation

/// `GET /api/live-destination/{id}`: where the truck's own navigation is taking it (design_preview key live_destination). Held in memory while
/// the Live screen shows it; never stored, logged or shared. `milesLeft` is always miles on this wire (absent when the car does not know it) and
/// `route` is an encoded polyline (precision 6).
struct LiveDestination: Decodable, Equatable {
    let name: String
    let lat: Double
    let lon: Double
    let eta: Date
    let milesLeft: Double?
    let route: String?
    let routePrecision: Int?

    /// A real fix (not 0,0 or out of range).
    var hasFix: Bool { lat.isFinite && lon.isFinite && !(lat == 0 && lon == 0) && abs(lat) <= 90 && abs(lon) <= 180 }

    /// The planned route's points ([] when absent or malformed).
    var routePoints: [DriveRoute.Point] {
        guard let route, !route.isEmpty else { return [] }
        return DriveRoute.decode(route, precision: routePrecision == 5 ? 5 : 6)
            .filter { abs($0.lat) <= 90 && abs($0.lon) <= 180 }
    }

    /// The row on the Live screen, as the web's: "To Work: 12 mi left, arrive 5:40 PM" (km on a metric phone; no distance when unknown).
    func line(metric: Bool = NowFormat.usesMetric, arrive: (Date) -> String = { $0.formatted(date: .omitted, time: .shortened) }) -> String {
        var parts: [String] = []
        if let miles = milesLeft, miles.isFinite, miles >= 0 {
            let v = metric ? miles * 1.609344 : miles
            parts.append("\(NumberText.fixed(v, v < 10 ? 1 : 0)) \(metric ? "km" : "mi") left")
        }
        parts.append("arrive \(arrive(eta))")
        return "To \(name): \(parts.joined(separator: ", "))"
    }
}

/// The three answers: a trip, no trip (204: clear it), and off (404: the feature is off for this account or the truck is not theirs, the same
/// answer for both, so the asking stops for the session).
enum LiveDestinationRead: Equatable {
    case trip(LiveDestination)
    case noTrip
    case off
}
