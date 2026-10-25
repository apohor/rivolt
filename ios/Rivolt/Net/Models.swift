import Foundation

/// Authenticated user as returned by `POST /api/auth/login` and
/// `GET /api/auth/me`. `userId` is present on `/me` but not on
/// the login response — optional so one type covers both.
struct AuthUser: Decodable, Equatable {
    let username: String
    let userId: String?
    /// The account's display units, when the server has the setting.
    var units: AccountUnits? = nil
}

/// `POST /api/auth/app/token`: the app session token (a bearer token, kept in
/// the Keychain) and who it signs in. Fixture: Fixtures/app_token.json, also
/// checked against the server's type in Go.
struct AppToken: Decodable, Equatable {
    let token: String
    let tokenType: String
    let expiresAt: Date
    let user: AuthUser
}

/// Vehicle metadata returned by `GET /api/vehicles`. We mirror only
/// the fields the home screen needs; the full struct (trim, images,
/// pack_kwh, ...) can be added as views start consuming it.
struct Vehicle: Decodable, Identifiable, Equatable {
    let id: String
    let vin: String
    let name: String
    let model: String
    let modelYear: Int?
    let imageUrl: String?
    /// The software versions, and whether a release-notes link exists for each (the link itself is fetched fresh when
    /// "Release notes" is tapped: Rivian's own expires after an hour).
    let ota: OTA?

    struct OTA: Decodable, Equatable {
        struct Version: Decodable, Equatable {
            let version: String?
            let hasNotes: Bool?
        }

        let current: Version?
        let available: Version?
    }
}

/// Point-in-time vehicle snapshot from `GET /api/state/{vehicleID}`.
/// Same rule as `Vehicle`: only the fields HomeView reads, more as
/// needed.
///
/// All units match the Go wire format (kilometers, kWh, Celsius).
/// Unit-convert at display time based on user preference — never in
/// the decoder, so debugging against raw responses stays sane.
struct VehicleState: Decodable, Equatable {
    let at: Date?
    let vehicleId: String
    let batteryLevelPct: Double
    let distanceToEmpty: Double   // kilometers
    let gear: String?
    let powerState: String?
    let chargerState: String?
    let chargerPowerKw: Double?
    let chargeTargetPct: Double?
    /// Vehicle speed in km/h; the Now card shows it while driving.
    let speedKph: Double?
    /// The pack size, to work out the time to the charge limit.
    let batteryCapacityKwh: Double?
    /// Tire pressures in bar (0 when a wheel doesn't report) and the software versions: the Truck tab's Vehicle card.
    /// The state's coordinates are not decoded: the app never shows or keeps where the truck is.
    let tirePressureFlBar: Double?
    let tirePressureFrBar: Double?
    let tirePressureRlBar: Double?
    let tirePressureRrBar: Double?
    let otaCurrentVersion: String?
    let otaAvailableVersion: String?
    /// The truck's own update state and install progress (an R1's feed; an R2 sends none), read by `NowUpdate` from a fixed word list.
    let otaStatus: String?
    let otaInstallProgress: Double?
    /// Rivian's own word for each wheel ("Normal", ...), shown only as theirs and only when it is one of the known low words.
    let tirePressureStatusFl: String?
    let tirePressureStatusFr: String?
    let tirePressureStatusRl: String?
    let tirePressureStatusRr: String?
    /// The Now card's comfort line: the cabin temperature in Celsius (0 when the truck hasn't reported one) and the
    /// truck's own words for preconditioning, pet mode and cabin (climate) hold. Read as given, never worked out.
    let cabinTempC: Double?
    let cabinPreconditioningStatus: String?
    let petModeStatus: String?
    let cabinHoldStatus: String?
    /// The truck's own time left in a charge (minutes; 0 or absent when it sends none) and whether it vouches for it
    /// ("invalid" hides it). The Now card prefers it to our estimate (see ChargeETA).
    let timeToEndMins: Double?
    let chargingTimeEstimationValidity: String?
}

/// `GET /api/vehicles/{id}/now` (now-contract v1): the server's view of the
/// truck's state and freshness, plus the current charge. Null means unknown,
/// never 0. Decoded strictly against the server's own type in
/// internal/api/now_ios_fixture_test.go, through the shared fixture.
struct NowSnapshot: Decodable, Equatable {
    enum Connection: String, Decodable {
        case live, replay, reauth, paused
    }

    enum State: String, Decodable {
        case driving, charging, plugged, parked, asleep, unknown
    }

    struct Charge: Decodable, Equatable {
        let startedAt: Date
        let socStart: Double?
        let peakKw: Double?
    }

    struct Ambient: Decodable, Equatable {
        let tempC: Double
        let source: String
        let asOf: Date
    }

    let version: Int
    let at: Date?
    let locationFixAt: Date?
    let connection: Connection
    let state: State
    /// Absent on servers older than the field.
    let asleep: Bool?
    let stateSince: Date?
    let socAtStateStart: Double?
    let tirePressureMinBar: Double?
    let failedSleeps6h: Int
    let vehicleStateFrameAt: Date?
    let charge: Charge?
    /// Only when the user turned outside temperature on.
    let ambient: Ambient?

    /// Keys as `.convertFromSnakeCase` produces them. It capitalizes each
    /// segment with `String.capitalized`, which turns "6h" into "6H", so
    /// `failed_sleeps_6h` arrives as `failedSleeps6H`, not `failedSleeps6h`.
    private enum CodingKeys: String, CodingKey {
        case version, at, locationFixAt, connection, state, asleep, stateSince
        case socAtStateStart, tirePressureMinBar
        case failedSleeps6h = "failedSleeps6H"
        case vehicleStateFrameAt, charge, ambient
    }
}
