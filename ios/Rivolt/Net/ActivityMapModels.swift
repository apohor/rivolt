import Foundation

/// One drive on the Activity map, from `GET /api/drives` (the list the web's Activity map reads): when, its two ends
/// and its stored trace. Only these fields are decoded (DA): no cost, place or the rest of the row. Held in memory
/// while the map is up (ActivityMapModel), never stored.
struct MapDrive: Decodable, Equatable, Identifiable {
    let id: String
    let startedAt: Date
    let startLat: Double
    let startLon: Double
    let endLat: Double
    let endLon: Double
    /// Google-encoded polyline (precision 5); empty or absent when the drive has no stored trace.
    let routePolyline: String?

    private enum CodingKeys: String, CodingKey {
        case id = "ID", startedAt = "StartedAt", startLat = "StartLat", startLon = "StartLon", endLat = "EndLat", endLon = "EndLon"
        case routePolyline = "RoutePolyline"
    }

    /// The stored trace, else a straight line between the ends; empty when there is nothing to draw.
    var points: [DriveRoute.Point] {
        DriveRoute.line(polyline: routePolyline, start: .init(lat: startLat, lon: startLon), end: .init(lat: endLat, lon: endLon))
    }
}

/// One charge on the Activity map, from `GET /api/charges`: when, where, its peak power and the ledger's kind (for
/// the web's power colours). Only these fields are decoded (DA). In memory only, like `MapDrive`.
struct MapCharge: Decodable, Equatable, Identifiable {
    let id: String
    let startedAt: Date
    let lat: Double
    let lon: Double
    let maxPowerKW: Double?
    /// The ledger's classification: "dcfc" or "ac"; absent when it couldn't classify.
    let kind: String?

    private enum CodingKeys: String, CodingKey {
        case id = "ID", startedAt = "StartedAt", lat = "Lat", lon = "Lon", maxPowerKW = "MaxPowerKW", kind
    }

    /// The web's power buckets (DriveMap.tsx chargeBucket): the ledger's DC fast charge (else a 50 kW peak when it
    /// couldn't classify), level 2 from 3 kW, level 1 below.
    enum Bucket: Int, CaseIterable {
        case fast, l2, l1

        var label: String {
            switch self {
            case .fast: return "Fast"
            case .l2: return "Level 2"
            case .l1: return "Household outlet (Level 1)"
            }
        }
    }

    var bucket: Bucket {
        if kind == "dcfc" { return .fast }
        let kw = maxPowerKW ?? .nan
        if kind == nil, kw.isFinite, kw >= 50 { return .fast }
        if !kw.isFinite || kw < 3 { return .l1 }
        return .l2
    }

    /// A real fix (not 0,0 or out of range).
    var hasFix: Bool { lat.isFinite && lon.isFinite && !(lat == 0 && lon == 0) && abs(lat) <= 90 && abs(lon) <= 180 }
}
