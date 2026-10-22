import Foundation

/// A milestone the truck reached (`GET /api/milestones`): once each per vehicle, in the server's own words. The
/// `detail` object isn't decoded; the card shows the title and body as given.
struct Milestone: Decodable, Equatable, Identifiable {
    let vehicleId: String
    let key: String
    let kind: String
    let achievedAt: Date
    let title: String
    let body: String

    var id: String { "\(vehicleId):\(key)" }
}

struct MilestonesResponse: Decodable, Equatable {
    let milestones: [Milestone]
}

/// `GET /api/settings/statement-email`: whether the server offers the monthly email and whether it is on.
struct StatementEmailSetting: Decodable, Equatable {
    let enabled: Bool
    let available: Bool
}
