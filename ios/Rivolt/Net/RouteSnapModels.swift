import Foundation

/// One telemetry sample of a drive's window from `GET /api/samples`, only what the drive screen needs (DA's fields):
/// when, where, how fast, the gear and when the GPS fix was taken (the route map); the battery %, the elevation and
/// the drive mode (the timeline). Range, odometer, temperatures and weather in the same rows are not decoded. Held in
/// memory while the drive's screen is open.
struct DriveSample: Decodable, Equatable {
    let at: Date
    let lat: Double
    let lon: Double
    let speedMph: Double?
    let shiftState: String?
    let locationFixAt: Date?
    /// The battery %; nil or 0 when the frame had none.
    var batteryLevelPct: Double? = nil
    /// Elevation in metres, for the power model's grade term.
    var altitudeM: Double? = nil
    /// Rivian's drive mode ("everyday", "sport", "distance"…).
    var driveMode: String? = nil
    /// The odometer in miles, for the Efficiency colour mode.
    var odometerMi: Double? = nil

    // altitude_m and drive_mode arrive snake_case: the decoder's convertFromSnakeCase turns them into these names.
    private enum CodingKeys: String, CodingKey {
        case at = "At", lat = "Lat", lon = "Lon", speedMph = "SpeedMph", shiftState = "ShiftState", locationFixAt = "LocationFixAt"
        case batteryLevelPct = "BatteryLevelPct", altitudeM, driveMode, odometerMi = "OdometerMi"
    }
}

/// `POST /api/maps/valhalla/trace_route` as the web sends it: walk_or_snap, OSRM format.
struct ValhallaTraceRequest: Encodable {
    struct Point: Encodable {
        let lat: Double
        let lon: Double
        /// Unix seconds; omitted when the point has no time.
        let time: Double?
    }

    struct TraceOptions: Encodable {
        let searchRadius = 100
        let gpsAccuracy = 25
    }

    let shape: [Point]
    let costing = "auto"
    let shapeMatch = "walk_or_snap"
    let traceOptions = TraceOptions()
    let format = "osrm"
}

/// The OSRM-format answer: the matched sub-routes (polyline6) and, per input point, which one it landed in.
struct ValhallaTraceResponse: Decodable {
    struct Matching: Decodable { let geometry: String? }
    struct Tracepoint: Decodable { let matchingsIndex: Int }

    let matchings: [Matching]?
    let tracepoints: [Tracepoint?]?
}

/// `POST /api/maps/valhalla/route` between two points, as the web fills a gap the matcher left.
struct ValhallaRouteRequest: Encodable {
    struct Location: Encodable {
        let lat: Double
        let lon: Double
        var type = "break"
    }

    struct DirectionsOptions: Encodable { let units = "miles" }

    let locations: [Location]
    let costing = "auto"
    let directionsOptions = DirectionsOptions()
}

struct ValhallaRouteResponse: Decodable {
    struct Trip: Decodable {
        struct Leg: Decodable { let shape: String? }
        let legs: [Leg]?
    }

    let trip: Trip?
}
