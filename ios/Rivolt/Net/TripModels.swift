import Foundation

// The trip planner's wire shapes. The plan itself is Rivian's, passed through by the server: its own keys are Go-cased
// (`Routes`, `Waypoints`, `TotalDriveDurationSec`) and it can gain fields without notice, so everything the screens do not
// need is left out and every field is read leniently (a missing or odd one is nil, never a failed screen). The wrappers around
// it (multi-day, saved trips, plan versus actual) are the server's own and snake_case.

/// A time the server may send as an empty string (an origin has no arrival).
struct LenientDate: Decodable, Equatable, Hashable {
    let date: Date?
    init(from decoder: Decoder) throws {
        let raw = try? decoder.singleValueContainer().decode(String.self)
        date = raw.flatMap { $0.isEmpty ? nil : JSON.parseDate($0) }
    }
}

/// `POST /api/trips/plan`: Rivian's plan (the routes it offers; the first is the recommended one) and our cost estimate per route.
struct TripPlan: Decodable, Equatable {
    var status: String?
    var chargeStationsAvailable: Bool?
    var soCBelowLimit: Bool?
    var routes: [TripRoute] = []
    var costs: [TripCost]? = nil

    enum CodingKeys: String, CodingKey {
        case status = "Status", chargeStationsAvailable = "ChargeStationsAvailable", soCBelowLimit = "SoCBelowLimit", routes = "Routes", costs
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        status = try? c.decode(String.self, forKey: .status)
        chargeStationsAvailable = try? c.decode(Bool.self, forKey: .chargeStationsAvailable)
        soCBelowLimit = try? c.decode(Bool.self, forKey: .soCBelowLimit)
        routes = (try? c.decode([TripRoute].self, forKey: .routes)) ?? []
        costs = try? c.decode([TripCost].self, forKey: .costs)
    }

    /// The route the planner recommends.
    var best: TripRoute? { routes.first }
}

struct TripRoute: Decodable, Equatable {
    var destinationReached: Bool?
    var totalChargingDurationSec: Double?
    var totalDriveDurationSec: Double?
    var totalTripDurationSec: Double?
    var totalDriveDistanceMeters: Double?
    var arrivalSoC: Double?
    var energyConsumptionKWh: Double?
    /// How far short of the destination the battery would run out, when it would.
    var batteryEmptyToDestMeters: Double?
    var waypoints: [TripWaypoint] = []

    enum CodingKeys: String, CodingKey {
        case destinationReached = "DestinationReached", totalChargingDurationSec = "TotalChargingDurationSec", totalDriveDurationSec = "TotalDriveDurationSec"
        case totalTripDurationSec = "TotalTripDurationSec", totalDriveDistanceMeters = "TotalDriveDistanceMeters", arrivalSoC = "ArrivalSoC"
        case energyConsumptionKWh = "EnergyConsumptionKWh", batteryEmptyToDestMeters = "BatteryEmptyToDestMeters", waypoints = "Waypoints"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        destinationReached = try? c.decode(Bool.self, forKey: .destinationReached)
        totalChargingDurationSec = try? c.decode(Double.self, forKey: .totalChargingDurationSec)
        totalDriveDurationSec = try? c.decode(Double.self, forKey: .totalDriveDurationSec)
        totalTripDurationSec = try? c.decode(Double.self, forKey: .totalTripDurationSec)
        totalDriveDistanceMeters = try? c.decode(Double.self, forKey: .totalDriveDistanceMeters)
        arrivalSoC = try? c.decode(Double.self, forKey: .arrivalSoC)
        energyConsumptionKWh = try? c.decode(Double.self, forKey: .energyConsumptionKWh)
        batteryEmptyToDestMeters = try? c.decode(Double.self, forKey: .batteryEmptyToDestMeters)
        waypoints = (try? c.decode([TripWaypoint].self, forKey: .waypoints)) ?? []
    }

    /// The charging stops, in order.
    var chargingStops: [TripWaypoint] { waypoints.filter { $0.isCharger } }
}

struct TripWaypoint: Decodable, Equatable, Identifiable {
    /// "origin", "destination", "charger" or a via/overnight the planner kept.
    var waypointType: String?
    var entityID: String?
    var name: String?
    var latitude: Double?
    var longitude: Double?
    var maxPowerKW: Double?
    var chargeDurationSec: Double?
    var arrivalSoC: Double?
    var departureSoC: Double?
    var adapterRequired: Bool?
    var arrivalTimeUTC: LenientDate?
    var departureTimeUTC: LenientDate?
    var distanceFromOriginMeters: Double?

    enum CodingKeys: String, CodingKey {
        case waypointType = "WaypointType", entityID = "EntityID", name = "Name", latitude = "Latitude", longitude = "Longitude"
        case maxPowerKW = "MaxPowerKW", chargeDurationSec = "ChargeDurationSec", arrivalSoC = "ArrivalSoC", departureSoC = "DepartureSoC"
        case adapterRequired = "AdapterRequired", arrivalTimeUTC = "ArrivalTimeUTC", departureTimeUTC = "DepartureTimeUTC"
        case distanceFromOriginMeters = "DistanceFromOriginMeters"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        waypointType = try? c.decode(String.self, forKey: .waypointType)
        entityID = try? c.decode(String.self, forKey: .entityID)
        name = try? c.decode(String.self, forKey: .name)
        latitude = try? c.decode(Double.self, forKey: .latitude)
        longitude = try? c.decode(Double.self, forKey: .longitude)
        maxPowerKW = try? c.decode(Double.self, forKey: .maxPowerKW)
        chargeDurationSec = try? c.decode(Double.self, forKey: .chargeDurationSec)
        arrivalSoC = try? c.decode(Double.self, forKey: .arrivalSoC)
        departureSoC = try? c.decode(Double.self, forKey: .departureSoC)
        adapterRequired = try? c.decode(Bool.self, forKey: .adapterRequired)
        arrivalTimeUTC = try? c.decode(LenientDate.self, forKey: .arrivalTimeUTC)
        departureTimeUTC = try? c.decode(LenientDate.self, forKey: .departureTimeUTC)
        distanceFromOriginMeters = try? c.decode(Double.self, forKey: .distanceFromOriginMeters)
    }

    var id: String { "\(waypointType ?? "")|\(entityID ?? "")|\(latitude ?? 0)|\(longitude ?? 0)|\(distanceFromOriginMeters ?? 0)" }
    var isCharger: Bool { waypointType == "charger" }
}

/// One route's cost estimate: what the fast-charging stops are expected to cost, set beside charging at home and burning gas.
struct TripCost: Decodable, Equatable {
    var currency: String?
    var dcfcSpendLow: Double?
    var dcfcSpendHigh: Double?
    var pricedStops: Int?
    var unpricedStops: Int?
    /// "observed" (typical prices seen by DCFC Tracker), a user's own rate, or none.
    var estimateBasis: String?
    var homeEquivalent: Double?
    var gasEquivalent: Double?

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        currency = try? c.decode(String.self, forKey: .currency)
        dcfcSpendLow = try? c.decode(Double.self, forKey: .dcfcSpendLow)
        dcfcSpendHigh = try? c.decode(Double.self, forKey: .dcfcSpendHigh)
        pricedStops = try? c.decode(Int.self, forKey: .pricedStops)
        unpricedStops = try? c.decode(Int.self, forKey: .unpricedStops)
        estimateBasis = try? c.decode(String.self, forKey: .estimateBasis)
        homeEquivalent = try? c.decode(Double.self, forKey: .homeEquivalent)
        gasEquivalent = try? c.decode(Double.self, forKey: .gasEquivalent)
    }

    enum CodingKeys: String, CodingKey { case currency, dcfcSpendLow, dcfcSpendHigh, pricedStops, unpricedStops, estimateBasis, homeEquivalent, gasEquivalent }
}

/// `POST /api/trips/plan-multiday`: the days, each with its own plan, and where a failure stopped it (the days before it come back).
struct TripMultiday: Decodable, Equatable {
    struct Total: Decodable, Equatable {
        var distanceMeters: Double?
        var driveDurationSec: Double?
        var chargingDurationSec: Double?
    }

    struct Place: Decodable, Equatable {
        var latitude: Double?
        var longitude: Double?
        var label: String?
    }

    struct Overnight: Decodable, Equatable {
        var name: String?
        var parkedHours: Double?
        var l2Kw: Double?
        var addedKwh: Double?
        var postChargeSocPct: Double?
        var capped: Bool?
    }

    struct Day: Decodable, Equatable, Identifiable {
        var index: Int
        var plan: TripPlan
        var departAt: LenientDate?
        var arriveAt: LenientDate?
        var start: Place?
        var end: Place?
        var departureSoc: Double?
        var arrivalSoc: Double?
        var destinationReached: Bool?
        var socBelowLimit: Bool?
        var overnight: Overnight?
        var id: Int { index }
    }

    var days: [Day] = []
    var total: Total?
    var tz: String?
    /// 1-based; set when a day could not be planned. `error` is the server's sentence for the person, `errorClass` its kind.
    var failedDay: Int?
    var error: String?
    var errorClass: String?
    var errorReason: String?

    var failed: Bool { failedDay != nil }
}

/// A saved trip. `inputs` and `plan` are blobs the client that saved it wrote, stored verbatim: the web and the phone save the same shapes
/// so each can read the other's, but a trip from anywhere else may not read at all, and then it is shown by name only.
struct SavedTrip: Decodable, Equatable, Identifiable {
    var id: String
    var name: String
    var inputs: JSONValue?
    var plan: JSONValue?
    var createdAt: Date?
    var updatedAt: Date?
}

/// Any JSON value, kept as it came (so a saved trip's blobs round-trip without the app knowing their shape).
enum JSONValue: Codable, Equatable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let b = try? c.decode(Bool.self) { self = .bool(b) }
        else if let n = try? c.decode(Double.self) { self = .number(n) }
        else if let s = try? c.decode(String.self) { self = .string(s) }
        else if let a = try? c.decode([JSONValue].self) { self = .array(a) }
        else { self = .object(try c.decode([String: JSONValue].self)) }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .number(let n): try c.encode(n)
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }

    /// Decodes this value as `T` (a saved trip's plan as a `TripPlan`, say); nil when it does not read.
    func decoded<T: Decodable>(_ type: T.Type) -> T? {
        guard let data = try? JSONEncoder().encode(self) else { return nil }
        return try? JSON.decoder().decode(T.self, from: data)
    }
}

// MARK: What the app sends

/// `POST /api/trips/plan`. Only what the phone's form sets: the web sends more (route preferences, connectors, networks), all optional.
struct TripPlanRequest: Encodable, Equatable {
    var vehicleId: String?
    /// Percent; omitted, the server uses the truck's live charge.
    var startingSoc: Double?
    /// The direction the truck faces at the start, in degrees (0 when unknown).
    var originBearing: Double = 0
    /// RFC 3339: when the trip leaves; omitted, now.
    var departAt: String?
    var waypoints: [TripPlanWaypoint]
    /// The charge to arrive with, percent.
    var targetArrivalSocPercent: Double?
    var driveMode: String?
    /// Whether the truck uses a Tesla adapter (CCS truck at a NACS charger).
    var hasAdapter: Bool?
    /// The usable pack, so the server can price the stops.
    var packKwh: Double?
}

struct TripPlanWaypoint: Encodable, Equatable {
    var latitude: Double
    var longitude: Double
    /// "origin", "destination" or "waypoint".
    var waypointType: String
    var entityId: String?
}

/// `POST` and `PUT /api/trips/saved`. The blobs are the web's: `inputs` has origin and destination as `{label, lat, lon}`, `pack_kwh`,
/// `starting_soc`; `plan` is the server's answer as it came.
struct SavedTripBody: Encodable, Equatable {
    var name: String
    var inputs: JSONValue
    var plan: JSONValue?
}

/// A place in a multi-day request.
struct TripPoint: Encodable, Equatable {
    var lat: Double
    var lon: Double
    var label: String?
}

/// One day of a multi-day trip: where it ends and, unless it is the last, the night that follows.
struct TripDayRequest: Encodable, Equatable {
    struct Overnight: Encodable, Equatable {
        /// Available AC power in kW at the stay.
        var l2Kw: Double
        /// Local time ("08:00") the truck is wanted the next morning.
        var until: String
        /// Cap on the charge after the night, percent.
        var maxSocPct: Double
        var name: String
    }

    var end: TripPoint
    var targetArrivalSocPct: Double?
    var overnight: Overnight?
}

/// `POST /api/trips/plan-multiday`. The truck, its charge and its pack are filled in by the server when left out.
struct TripMultidayRequest: Encodable, Equatable {
    var vehicleId: String?
    var startingSoc: Double?
    var packKwh: Double?
    /// RFC 3339.
    var startAt: String
    /// The phone's zone, which the "until" times and the day boundaries are read in.
    var tz: String
    var targetArrivalSocPercent: Double?
    var origin: TripPoint
    var days: [TripDayRequest]
    var maxOvernightSocPct: Double?
}

/// `POST /api/trips/overnight-candidates`: stays near where a day's driving budget runs out.
struct OvernightCandidatesRequest: Encodable, Equatable {
    var path: [[Double]]
    var driveSec: Double?
    var budgetHours: Double?
    var kinds: [String]?
}

struct OvernightCandidates: Decodable, Equatable {
    struct Candidate: Decodable, Equatable, Identifiable {
        var name: String
        var lat: Double
        var lon: Double
        var l2Kw: Double?
        var l2KwAssumed: Bool?
        /// The round trip off the route, in metres.
        var detourM: Double?
        var kind: String?
        var id: String { "\(lat),\(lon),\(name)" }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            name = (try? c.decode(String.self, forKey: .name)) ?? "Stay"
            lat = try c.decode(Double.self, forKey: .lat)
            lon = try c.decode(Double.self, forKey: .lon)
            l2Kw = try? c.decode(Double.self, forKey: .l2Kw)
            l2KwAssumed = try? c.decode(Bool.self, forKey: .l2KwAssumed)
            detourM = try? c.decode(Double.self, forKey: .detourM)
            kind = try? c.decode(String.self, forKey: .kind)
        }

        init(name: String, lat: Double, lon: Double, l2Kw: Double? = nil, l2KwAssumed: Bool? = nil, detourM: Double? = nil, kind: String? = nil) {
            self.name = name; self.lat = lat; self.lon = lon; self.l2Kw = l2Kw; self.l2KwAssumed = l2KwAssumed; self.detourM = detourM; self.kind = kind
        }

        private enum CodingKeys: String, CodingKey { case name, lat, lon, l2Kw, l2KwAssumed, detourM, kind }
    }

    struct Target: Decodable, Equatable {
        /// True when the whole trip fits in the driving budget, so no night is needed.
        var reachesEnd: Bool?
    }

    var candidates: [Candidate] = []
    var target: Target?
    var attribution: String?

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        candidates = (try? c.decode([Candidate].self, forKey: .candidates)) ?? []
        target = try? c.decode(Target.self, forKey: .target)
        attribution = try? c.decode(String.self, forKey: .attribution)
    }

    init(candidates: [Candidate] = [], target: Target? = nil, attribution: String? = nil) {
        self.candidates = candidates; self.target = target; self.attribution = attribution
    }

    private enum CodingKeys: String, CodingKey { case candidates, target, attribution }
}
