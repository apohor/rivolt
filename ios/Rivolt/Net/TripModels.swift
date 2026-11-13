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
    /// The server's weather correction (leg i runs from waypoint i to i+1 of the first route); nil when weather is off or no leg had a forecast.
    var weatherAdjustment: TripWeatherAdjustment? = nil
    /// The terrain under the first route, for the elevation chart; nil when the server sent none.
    var elevationProfile: ElevationProfile? = nil
    /// The destination's IANA zone ("America/Denver"); absent when the coordinates name none, and on plans from before it existed.
    var destinationTz: String?
    /// The answer exactly as it came, for saving it back as the web does.
    var raw: JSONValue?

    enum CodingKeys: String, CodingKey {
        case destinationTz, weatherAdjustment, elevationProfile, status = "Status", chargeStationsAvailable = "ChargeStationsAvailable", soCBelowLimit = "SoCBelowLimit", routes = "Routes", costs
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        status = try? c.decode(String.self, forKey: .status)
        chargeStationsAvailable = try? c.decode(Bool.self, forKey: .chargeStationsAvailable)
        soCBelowLimit = try? c.decode(Bool.self, forKey: .soCBelowLimit)
        routes = (try? c.decode([TripRoute].self, forKey: .routes)) ?? []
        costs = try? c.decode([TripCost].self, forKey: .costs)
        destinationTz = try? c.decode(String.self, forKey: .destinationTz)
        weatherAdjustment = try? c.decode(TripWeatherAdjustment.self, forKey: .weatherAdjustment)
        elevationProfile = try? c.decode(ElevationProfile.self, forKey: .elevationProfile)
        raw = try? JSONValue(from: decoder)
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
    /// The route's stored shape (encoded, precision 6), when the server has one (the demo's).
    var routePolyline: String?

    enum CodingKeys: String, CodingKey {
        case routePolyline, geometry = "Geometry", destinationReached = "DestinationReached", totalChargingDurationSec = "TotalChargingDurationSec", totalDriveDurationSec = "TotalDriveDurationSec"
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
        // The server's own line through every stop (polyline6) when it sent one; else the stored shape (the demo's).
        let geometry = try? c.decode(String.self, forKey: .geometry)
        routePolyline = (try? c.decode(String.self, forKey: .routePolyline)) ?? geometry
    }

    /// The charging stops, in order.
    var chargingStops: [TripWaypoint] { waypoints.filter { $0.isCharger } }
}

struct TripWaypoint: Decodable, Equatable, Identifiable {
    /// Rivian's own word, in any case: "ORIGIN", "DESTINATION", "DC_CHARGE_STATION" for a charging stop, "OTHER" or "WAYPOINT" for a via.
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
    /// Where the stop came from, set by the server: "origin", "destination", "via" (a place the request asked to pass), "planner" (a charging stop the
    /// planner chose) or "overnight". Absent on plans stored before it existed.
    var source: String?

    enum CodingKeys: String, CodingKey {
        case source = "Source", waypointType = "WaypointType", entityID = "EntityID", name = "Name", latitude = "Latitude", longitude = "Longitude"
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
        source = try? c.decode(String.self, forKey: .source)
    }

    var id: String { "\(waypointType ?? "")|\(entityID ?? "")|\(latitude ?? 0)|\(longitude ?? 0)|\(distanceFromOriginMeters ?? 0)" }
    /// A charging stop is any waypoint that is not the origin, the destination or a via, in any case: the web's rule, so the same plan
    /// shows the same stops on both. (Rivian says "DC_CHARGE_STATION"; this once looked for "charger" and found none.)
    var isCharger: Bool {
        guard let t = waypointType?.lowercased() else { return false }
        return !["origin", "destination", "waypoint", "other"].contains(t)
    }
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
    /// One entry per charging stop, in the order of the route's stops.
    var breakdown: [Stop]?

    struct Stop: Decodable, Equatable {
        var networkName: String?
        var chargerName: String?
        var energyKwh: Double?
        /// Nil when the stop has no price ("price shown at the charger").
        var costLow: Double?
        var costHigh: Double?
    }

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
        breakdown = try? c.decode([Stop].self, forKey: .breakdown)
    }

    enum CodingKeys: String, CodingKey { case currency, dcfcSpendLow, dcfcSpendHigh, pricedStops, unpricedStops, estimateBasis, homeEquivalent, gasEquivalent, breakdown }
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
        /// The day's fast-charging cost estimate, with a price per stop.
        var costs: [TripCost]?
        /// The IANA zone where this day ends.
        var endTz: String?
        /// The day's weather: the server puts it beside the day's plan, not in it.
        var weatherAdjustment: TripWeatherAdjustment?
        var id: Int { index }
    }

    var days: [Day] = []
    var total: Total?
    var tz: String?
    /// The final destination's IANA zone.
    var destinationTz: String?
    /// 1-based; set when a day could not be planned. `error` is the server's sentence for the person, `errorClass` its kind.
    var failedDay: Int?
    var error: String?
    var errorClass: String?
    var errorReason: String?
    /// The answer exactly as it came, for saving it back as the web does.
    var raw: JSONValue?

    enum CodingKeys: String, CodingKey { case days, total, tz, destinationTz, failedDay, error, errorClass, errorReason }

    init() {}

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        days = (try? c.decode([Day].self, forKey: .days)) ?? []
        total = try? c.decode(Total.self, forKey: .total)
        tz = try? c.decode(String.self, forKey: .tz)
        destinationTz = try? c.decode(String.self, forKey: .destinationTz)
        failedDay = try? c.decode(Int.self, forKey: .failedDay)
        error = try? c.decode(String.self, forKey: .error)
        errorClass = try? c.decode(String.self, forKey: .errorClass)
        errorReason = try? c.decode(String.self, forKey: .errorReason)
        raw = try? JSONValue(from: decoder)
    }

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
    /// On a free account, a trip past the free cap: it can be opened and deleted, not edited. Absent otherwise.
    var readOnly: Bool?

    init(id: String, name: String, inputs: JSONValue? = nil, plan: JSONValue? = nil, createdAt: Date? = nil, updatedAt: Date? = nil, readOnly: Bool? = nil) {
        self.id = id; self.name = name; self.inputs = inputs; self.plan = plan; self.createdAt = createdAt; self.updatedAt = updatedAt; self.readOnly = readOnly
    }

    private enum CodingKeys: String, CodingKey { case id, name, inputs, plan, createdAt, updatedAt, readOnly }
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
    var entityId: String?
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

    /// Stops on the way to this day's end (a fast charger to start the morning with, say).
    var vias: [TripPoint]?
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

    /// A listed stay found outside the searched windows, with how far from the day's end it is.
    struct Nearest: Decodable, Equatable {
        var name: String?
        /// Driving time from the start to the stay.
        var driveSec: Double?
        /// Driving time from the budget point: negative before it (a shorter day), positive after it.
        var offsetSec: Double?
    }

    struct NearestStays: Decodable, Equatable {
        var before: Nearest?
        var after: Nearest?
    }

    /// The nearest DC fast charger to a stay with none of its own: where the next morning starts.
    struct FastCharger: Decodable, Equatable {
        var name: String?
        var lat: Double?
        var lon: Double?
        var entityId: String?
        var distanceM: Double?
        var maxPowerKw: Double?
    }

    /// A hotel with no charger listed (OpenStreetMap), offered only when no stay with a listed charger is in range.
    struct FallbackStay: Decodable, Equatable, Identifiable {
        var name: String
        var lat: Double
        var lon: Double
        var locality: String?
        var fastCharger: FastCharger?
        var detourM: Double?
        var id: String { "\(lat),\(lon),\(name)" }
    }

    struct Target: Decodable, Equatable {
        /// True when the whole trip fits in the driving budget, so no night is needed.
        var reachesEnd: Bool?
        /// How far along the route the driving budget runs out, in metres.
        var alongM: Double?
        /// Minutes of driving before and after that point that the search covered (the widest tried, when it found nothing).
        var windowBeforeMin: Double?
        var windowAfterMin: Double?
    }

    var candidates: [Candidate] = []
    var nearest: NearestStays?
    var fallbackStays: [FallbackStay] = []
    /// The route's length in metres.
    var routeM: Double?
    var target: Target?
    var attribution: String?
    var attributionLicenseUrl: String?
    var attributionSourceUrl: String?

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        candidates = (try? c.decode([Candidate].self, forKey: .candidates)) ?? []
        nearest = try? c.decode(NearestStays.self, forKey: .nearest)
        fallbackStays = (try? c.decode([FallbackStay].self, forKey: .fallbackStays)) ?? []
        target = try? c.decode(Target.self, forKey: .target)
        routeM = try? c.decode(Double.self, forKey: .routeM)
        attribution = try? c.decode(String.self, forKey: .attribution)
        attributionLicenseUrl = try? c.decode(String.self, forKey: .attributionLicenseUrl)
        attributionSourceUrl = try? c.decode(String.self, forKey: .attributionSourceUrl)
    }

    init(candidates: [Candidate] = [], target: Target? = nil, attribution: String? = nil, routeM: Double? = nil, nearest: NearestStays? = nil,
         fallbackStays: [FallbackStay] = [], attributionLicenseUrl: String? = nil, attributionSourceUrl: String? = nil) {
        self.candidates = candidates; self.target = target; self.attribution = attribution; self.routeM = routeM; self.nearest = nearest
        self.fallbackStays = fallbackStays; self.attributionLicenseUrl = attributionLicenseUrl; self.attributionSourceUrl = attributionSourceUrl
    }

    private enum CodingKeys: String, CodingKey {
        case candidates, target, attribution, routeM, nearest, fallbackStays, attributionLicenseUrl, attributionSourceUrl
    }
}

/// `POST /api/maps/chargers-along`: the chargers within a band either side of a route. The keys are the server's own camel case.
struct ChargersAlongRequest: Encodable, Equatable {
    var path: [[Double]]
    /// "dcfc" for DC fast chargers.
    var filter: String
    var minPowerKw: Double?
}

struct AlongCharger: Decodable, Equatable, Identifiable {
    var lat: Double
    var lon: Double
    var name: String?
    var isDCFC: Bool?
    var maxPowerKW: Double?
    var evNetwork: String?
    /// What a layer pin is ("dcfc", "hotel", "camp"), whether its network is one the person prefers, and how far off the route it is (km, exact
    /// from the corridor layers); nil on the plain fast-charger list.
    var kind: String? = nil
    var preferred = false
    var offKm: Double? = nil
    var networkName: String? = nil
    var id: String { "\(lat),\(lon),\(name ?? "")" }

    private enum CodingKeys: String, CodingKey { case lat, lon, name, isDCFC, maxPowerKW, evNetwork }
}

/// `POST /api/maps/corridor-layers`: the charger, hotel and campground layers along a whole route, in route order, from the server's archive. Nothing
/// is live: `asof` is when the archive was built.
struct CorridorLayersRequest: Encodable, Equatable {
    var path: [[Double]]
    /// Any of "dcfc", "l2", "hotels", "camp".
    var kinds: [String]
    var limit: Int?
    /// The person's Trip planner settings, sent as they are (absent for a server or an account without them): how far off the route ("close", "normal",
    /// "wide"), how many miles from the start to hide chargers (0, 25, 50), and the smallest fast charger's power (0, 50, 150, 250).
    var preset: String? = nil
    var startExcludeMi: Int? = nil
    var minFastKw: Int? = nil
}

struct CorridorPOI: Decodable, Equatable, Identifiable {
    var lat: Double
    var lon: Double
    var name: String?
    var isDCFC: Bool?
    var maxPowerKW: Double?
    var evNetwork: String?
    var networkSlug: String?
    var networkName: String?
    var alongKm: Double?
    var offKm: Double?
    var kinds: [String]?
    var id: String { "\(lat),\(lon),\(name ?? "")" }
}

struct CorridorLayers: Decodable, Equatable {
    var pois: [CorridorPOI] = []
    var total: Int = 0
    var asof: String?
    var nextAfterKm: Double?

    init(pois: [CorridorPOI] = [], total: Int = 0, asof: String? = nil, nextAfterKm: Double? = nil) {
        self.pois = pois; self.total = total; self.asof = asof; self.nextAfterKm = nextAfterKm
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        pois = (try? c.decode([CorridorPOI].self, forKey: .pois)) ?? []
        total = (try? c.decode(Int.self, forKey: .total)) ?? pois.count
        asof = try? c.decode(String.self, forKey: .asof)
        nextAfterKm = try? c.decode(Double.self, forKey: .nextAfterKm)
    }

    private enum CodingKeys: String, CodingKey { case pois, total, asof, nextAfterKm }
}

struct ChargersAlong: Decodable, Equatable {
    var chargers: [AlongCharger] = []

    init(chargers: [AlongCharger] = []) { self.chargers = chargers }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        chargers = (try? c.decode([AlongCharger].self, forKey: .chargers)) ?? []
    }

    private enum CodingKeys: String, CodingKey { case chargers }
}

/// `GET /api/demo/trips`: the sample trips the demo offers as read-only examples (the web's /sample-trips), each with the figures its
/// card shows and the shape of its route. Static on the server: no planner call, nothing stored.
struct DemoTripPreset: Decodable, Equatable, Identifiable {
    struct Summary: Decodable, Equatable {
        var distanceMi: Double?
        var durationMin: Double?
        var chargeStops: Int?
        var arrivalSocPct: Double?
    }

    var id: String
    var name: String
    var inputs: JSONValue?
    var summary: Summary?
    /// The route's shape (encoded, precision 6), so the map follows the roads with no routing call.
    var routePolyline: String?
}

struct DemoTrips: Decodable, Equatable {
    var trips: [DemoTripPreset] = []

    init(trips: [DemoTripPreset] = []) { self.trips = trips }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        trips = (try? c.decode([DemoTripPreset].self, forKey: .trips)) ?? []
    }

    private enum CodingKeys: String, CodingKey { case trips }
}

/// `POST /api/trips/what-if`: the preview of one edit to the plan on screen (a stop added, moved or removed), worked out from our own routing with no
/// call to Rivian. Numbers are estimates: the time and distance the edit adds, the points of charge lost by the next charging stop, and one-way
/// warnings. Rivian re-plans on confirm and may change the stops.
struct WhatIfStop: Encodable, Equatable {
    var lat: Double
    var lon: Double
    /// "origin" | "via" | "planner" | "overnight" | "destination".
    var source: String
    var arrivalAt: String?
    var arrivalSoc: Double?
    var departureAt: String?
    var departureSoc: Double?
    var distanceFromOriginM: Double?
}

struct WhatIfPoint: Encodable, Equatable {
    var lat: Double
    var lon: Double
}

struct WhatIfEdit: Encodable, Equatable {
    /// "add" (before `stops[index]`), "move" or "remove" (the via at `index`).
    var kind: String
    var index: Int
    var via: WhatIfPoint?
}

struct WhatIfRequest: Encodable, Equatable {
    var stops: [WhatIfStop]
    var edit: WhatIfEdit
    var targetArrivalSocPercent: Double?
}

struct WhatIfResponse: Decodable, Equatable {
    struct Segment: Decodable, Equatable { var from: Int; var to: Int }
    struct At: Decodable, Equatable { var arrivalAt: String? }
    struct NextCharge: Decodable, Equatable { var index: Int?; var isDestination: Bool?; var socDropPts: Double? }

    var estimated: Bool?
    var notice: String?
    var segment: Segment?
    /// The edited stretch only (polyline6), to draw between `segment.from` and `segment.to`; absent when the routing failed.
    var geometry: String?
    var addedM: Double?
    var addedSec: Double?
    var atEdit: At?
    var destination: At?
    var nextCharge: NextCharge?
    var warnings: [String]?
}


/// `weather_adjustment` on a plan: the per-leg weather and the energy multiplier the server applied to each leg (weather, times the driver profile when the
/// plan used one). Every leg field is optional: a leg with no forecast has only its multiplier.
struct TripWeatherAdjustment: Decodable, Equatable {
    var legs: [TripWeatherLeg]
    /// The trip-wide driver-profile multiplier; 1 or absent means the profile had no effect.
    var profileMultiplier: Double?

}

struct TripWeatherLeg: Decodable, Equatable {
    var multiplier: Double
    /// "Feels like" when the forecast has one, else the air temperature.
    var tempC: Double?
    /// The plain air temperature (what the truck shows).
    var airTempC: Double?
    /// Positive: against the way you drive; negative: with it.
    var headwindKph: Double?
    var precipMm: Double?
    /// True only when the forecast says snow.
    var snow: Bool?
    var climbM: Double?
    var descentM: Double?

}


/// `elevation_profile` on a plan: the terrain along the first route as [distance in metres, elevation in metres] points (200 at most), and where each
/// waypoint sits along it in metres (null for one that could not be placed). Context only: no energy figure comes from it.
struct ElevationProfile: Decodable, Equatable {
    var points: [[Double]]
    var waypointAtM: [Double?]
}
