import Foundation

/// Authenticated user as returned by `POST /api/auth/login` and
/// `GET /api/auth/me`. `userId` is present on `/me` but not on
/// the login response — optional so one type covers both.
struct AuthUser: Decodable, Equatable {
    let username: String
    let userId: String?
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
    let latitude: Double?
    let longitude: Double?
}
