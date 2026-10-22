import Foundation
import Observation

/// The reads behind the milestone and "Three things" cards. Kept apart from `APIClient` so tests can fake them.
protocol ExtrasSource: Sendable {
    func milestones() async throws -> [Milestone]
    func dismissMilestone(vehicleID: String, key: String) async throws
    func hasLease(vehicleID: String) async throws -> Bool
    func statementEmail() async throws -> StatementEmailSetting
}

extension APIClient: ExtrasSource {}

/// One row of the "Three things you may not have turned on" card.
enum ThreeThingsRow: String, Equatable, Identifiable {
    case push, lease, statement

    var id: String { rawValue }

    var label: String {
        switch self {
        case .push: return "Charge-done notifications"
        case .lease: return "Lease tracker"
        case .statement: return "Monthly statement by email"
        }
    }

    var hint: String {
        switch self {
        case .push: return "A notification when charging finishes."
        case .lease: return "Enter your allowance and term to see projected miles at lease return and the overage cost."
        case .statement: return "On the 1st of each month, last month's cost, split and biggest charges."
        }
    }

    var cta: String {
        switch self {
        case .push, .statement: return "Turn on"
        case .lease: return "Add lease"
        }
    }

    /// A thing the account already has is left out. The phone's own notification state decides the first row (not the
    /// web's push status); a read that failed (nil) leaves its row out rather than guessing; the statement row only
    /// shows where the server offers the email.
    static func rows(pushNeeded: Bool, hasLease: Bool?, statement: StatementEmailSetting?) -> [ThreeThingsRow] {
        var rows: [ThreeThingsRow] = []
        if pushNeeded { rows.append(.push) }
        if hasLease == false { rows.append(.lease) }
        if let statement, statement.available, !statement.enabled { rows.append(.statement) }
        return rows
    }
}

/// Loads the milestone and "Three things" data, one truck at a time. A failed read leaves its part empty.
@MainActor
@Observable
final class ExtrasModel {
    static let hiddenKey = "rivolt.threeThings.dismissed"

    private(set) var milestones: [Milestone] = []
    private(set) var hasLease: Bool?
    private(set) var statement: StatementEmailSetting?
    private(set) var loaded = false
    private(set) var dismissFailed = false
    private(set) var threeThingsHidden: Bool
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private var hiding: Set<String> = []
    private(set) var hidingVersion = 0

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        threeThingsHidden = defaults.bool(forKey: Self.hiddenKey)
    }

    func load(source: some ExtrasSource, vehicleID: String) async {
        async let m = try? source.milestones()
        async let l = try? source.hasLease(vehicleID: vehicleID)
        async let s = try? source.statementEmail()
        let (mm, ll, ss) = await (m, l, s)
        milestones = mm ?? []
        hasLease = ll
        statement = ss
        loaded = true
    }

    /// The newest milestone for this truck that hasn't been hidden, one at a time.
    func current(vehicleID: String) -> Milestone? {
        _ = hidingVersion
        return milestones.first { $0.vehicleId == vehicleID && !hiding.contains($0.id) }
    }

    /// Gone at once; if the server refuses, it comes back with a message.
    func dismiss(_ m: Milestone, source: some ExtrasSource) async {
        dismissFailed = false
        hiding.insert(m.id)
        hidingVersion += 1
        do {
            try await source.dismissMilestone(vehicleID: m.vehicleId, key: m.key)
        } catch {
            hiding.remove(m.id)
            hidingVersion += 1
            dismissFailed = true
        }
    }

    /// Kept on this phone only: nothing is sent.
    func hideThreeThings() {
        threeThingsHidden = true
        defaults.set(true, forKey: Self.hiddenKey)
    }

    func rows(pushNeeded: Bool) -> [ThreeThingsRow] {
        guard loaded, !threeThingsHidden else { return [] }
        return ThreeThingsRow.rows(pushNeeded: pushNeeded, hasLease: hasLease, statement: statement)
    }
}
