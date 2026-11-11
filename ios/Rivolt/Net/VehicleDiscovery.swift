import Foundation

/// `GET /api/vehicles/discovery`: why a linked Rivian account has no vehicle on file yet, in the server's order of precedence: unlinked, reauth (Rivian needs
/// a new sign-in: never "looking"), found, none, looking (a failed lookup is looking too: the server retries it).
struct VehicleDiscovery: Decodable, Equatable {
    var state: String
}

/// `POST /api/settings/rivian/refresh-vehicles`: how many vehicles the account lists now.
struct RivianVehicleCount: Decodable, Equatable {
    var vehicleCount: Int
    enum CodingKeys: String, CodingKey { case vehicleCount = "vehicle_count" }
}

enum VehicleDiscoveryState: Equatable {
    case unlinked, reauth, found, empty, looking

    /// The state from what the request gave back: the answer, a 503 as looking, and anything else (a 404 where the feature is off, no network) as no word (nil),
    /// so the screen keeps its older wording.
    static func from(_ answer: VehicleDiscovery?, error: Error?) -> VehicleDiscoveryState? {
        if let answer {
            switch answer.state {
            case "unlinked": return .unlinked
            case "reauth": return .reauth
            case "found": return .found
            case "none": return .empty
            case "looking": return .looking
            default: return nil
            }
        }
        if case APIError.server(503, _, _)? = error { return .looking }
        return nil
    }

    /// What the connect screen says for this state; nil where the older screen is right (not linked, found, no word).
    var view: DiscoveryView? {
        switch self {
        case .looking: return DiscoveryView(kind: .looking, title: VehicleDiscoveryCopy.lookingTitle, body: VehicleDiscoveryCopy.lookingBody, action: nil)
        case .empty: return DiscoveryView(kind: .noVehicles, title: VehicleDiscoveryCopy.noneTitle, body: VehicleDiscoveryCopy.noneBody, action: VehicleDiscoveryCopy.noneAction)
        case .reauth: return DiscoveryView(kind: .reauth, title: VehicleDiscoveryCopy.reauthTitle, body: VehicleDiscoveryCopy.reauthBody, action: VehicleDiscoveryCopy.reauthAction)
        case .unlinked, .found: return nil
        }
    }
}

struct DiscoveryView: Equatable {
    enum Kind: Equatable { case looking, noVehicles, reauth }
    var kind: Kind
    var title: String
    var body: String
    var action: String?
}
