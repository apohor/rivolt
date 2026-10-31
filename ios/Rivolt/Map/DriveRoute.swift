import CoreGraphics
import Foundation

/// The route of one drive from `GET /api/drives/{id}`, as the map draws it: the recorded GPS trace and its two ends.
/// Held only while the drive's screen is open (never in the App Group, UserDefaults or a file). The rest of the
/// drive row is not decoded.
struct DriveRoute: Decodable, Equatable {
    struct Point: Equatable {
        let lat: Double
        let lon: Double
    }

    let vehicleID: String?
    let startedAt: Date?
    let endedAt: Date?
    let startLat: Double
    let startLon: Double
    let endLat: Double
    let endLon: Double
    /// Google-encoded polyline (precision 5); empty when the drive has no stored trace.
    let routePolyline: String?
    /// The drive moved but its truck sent no usable live location, so it has no route. Absent on an older server.
    var locationUnavailable: Bool? = nil

    private enum CodingKeys: String, CodingKey {
        case vehicleID = "VehicleID", startedAt = "StartedAt", endedAt = "EndedAt"
        case startLat = "StartLat", startLon = "StartLon", endLat = "EndLat", endLon = "EndLon", routePolyline = "RoutePolyline", locationUnavailable = "LocationUnavailable"
    }

    /// The trace, or a straight line between the ends when there is none (as the web). Points at 0,0 (no fix) are
    /// dropped; nothing to draw is an empty list.
    var points: [Point] {
        DriveRoute.line(polyline: routePolyline, start: Point(lat: startLat, lon: startLon), end: Point(lat: endLat, lon: endLon))
    }

    /// A drive's line from its stored trace, else its two ends (the drive map and the Activity map alike).
    static func line(polyline: String?, start: Point, end: Point) -> [Point] {
        let trace = decode(polyline ?? "")
        let line = trace.count >= 2 ? trace : [start, end]
        let real = line.filter { !($0.lat == 0 && $0.lon == 0) && abs($0.lat) <= 90 && abs($0.lon) <= 180 }
        return real.count >= 2 ? real : []
    }

    /// Google's encoded-polyline format at precision 5 (the server's internal/rivian/polyline.go, the web's
    /// decodePolyline). A truncated string returns the points decoded so far.
    static func decode(_ s: String, precision: Int = 5) -> [Point] {
        let factor = precision == 6 ? 1e-6 : 1e-5
        let bytes = Array(s.utf8)
        var out: [Point] = []
        var i = 0, lat = 0, lon = 0
        func next() -> Int? {
            var result = 0, shift = 0
            while true {
                guard i < bytes.count else { return nil }
                let b = Int(bytes[i]) - 63
                i += 1
                result |= (b & 0x1f) << shift
                shift += 5
                if b < 0x20 { break }
            }
            return (result & 1) != 0 ? ~(result >> 1) : result >> 1
        }
        while i < bytes.count {
            guard let dlat = next(), let dlon = next() else { return out }
            lat += dlat
            lon += dlon
            out.append(Point(lat: Double(lat) * factor, lon: Double(lon) * factor))
        }
        return out
    }
}
