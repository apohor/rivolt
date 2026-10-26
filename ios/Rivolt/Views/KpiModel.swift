import Foundation
import Observation

/// The reads behind the tiles: the month's drives and charges, the ledger's spend and the drives since the last charge.
/// Kept apart from `APIClient` so tests can fake them.
protocol KpiSource: Sendable {
    func activity(since: Date, until: Date, vehicleID: String) async throws -> ActivityResponse
    func ledgerSpend(month: String, timeZone: String, vehicleID: String?) async throws -> LedgerMonthSpend
    func chartPoints(since: Date, until: Date, vehicleID: String) async throws -> ChartPoints
}

extension APIClient: KpiSource {}

/// Loads the tiles' data and keeps each answer in memory for 15 minutes per truck and month (the server groups up to 5,000
/// rows for every call and rate-limits it). Pull-to-refresh forces a read. A failed read, a 429 included, keeps what is
/// on screen for the same truck and month; another month never shows this month's figures. The three reads fail apart,
/// so one missing leaves only its tiles as a dash.
@MainActor
@Observable
final class KpiModel {
    static let lifetime: TimeInterval = 15 * 60

    private(set) var activity: ActivityResponse?
    private(set) var ledger: LedgerMonthSpend?
    private(set) var ledgerPending = true
    private(set) var since: SinceCharge?
    private(set) var isLoading = false

    private struct Entry {
        var activity: ActivityResponse?
        var ledger: LedgerMonthSpend?
        var since: SinceCharge?
        var fetchedAt: Date
    }
    @ObservationIgnored private var cache: [String: Entry] = [:]
    @ObservationIgnored private var shownKey: String?

    init(activity: ActivityResponse? = nil, ledger: LedgerMonthSpend? = nil, since: SinceCharge? = nil) {
        self.activity = activity
        self.ledger = ledger
        self.since = since
        ledgerPending = ledger == nil && activity == nil
    }

    /// `lastChargeEnd` is when the newest finished charge ended (nil when there is none); `scopeLedger` sends the truck
    /// to the ledger only for an account with more than one.
    func load(source: some KpiSource, vehicleID: String, month: PeriodMonth, scopeLedger: Bool, lastChargeEnd: Date?,
              force: Bool = false, now: Date = .now, calendar: Calendar = .current) async {
        let sinceKey = lastChargeEnd.map { String($0.timeIntervalSince1970) } ?? "none"
        let key = "\(vehicleID)|\(month.key)|\(sinceKey)"
        if !force, let hit = cache[key], now.timeIntervalSince(hit.fetchedAt) < Self.lifetime {
            apply(hit)
            shownKey = key
            return
        }
        if shownKey != key {
            // Another month, truck or charge: never leave the last one's figures under it while this loads.
            activity = nil; ledger = nil; since = nil; ledgerPending = true
            shownKey = key
        }
        isLoading = true
        defer { isLoading = false }
        let range = month.range(calendar: calendar)
        var ymd = Date.FormatStyle(timeZone: calendar.timeZone).year().month(.twoDigits)
        ymd.calendar = calendar
        let monthID = range.since.formatted(ymd).replacingOccurrences(of: "/", with: "-")
        let tz = calendar.timeZone.identifier
        let tomorrow = calendar.date(byAdding: .day, value: 1, to: calendar.startOfDay(for: now)) ?? now
        async let a = try? source.activity(since: range.since, until: range.until, vehicleID: vehicleID)
        // The ledger's statement is a calendar month's; any other period's spend is worked out from its own charges (KpiLogic).
        let isMonth = month.kind == .month
        async let l: LedgerMonthSpend? = isMonth ? try? source.ledgerSpend(month: monthID, timeZone: tz, vehicleID: scopeLedger ? vehicleID : nil) : nil
        async let p = Self.drivesSince(source, end: lastChargeEnd, until: tomorrow, vehicleID: vehicleID)
        let (aa, ll, pp) = await (a, l, p)
        var entry = cache[key] ?? Entry(activity: nil, ledger: nil, since: nil, fetchedAt: .distantPast)
        // A read that failed keeps the earlier answer for this same key.
        if let aa { entry.activity = aa }
        if let ll { entry.ledger = ll }
        if let end = lastChargeEnd, let pp { entry.since = KpiLogic.sinceCharge(end: end, points: pp) }
        // Only a complete answer is fresh; a failed part is read again on the next load.
        if aa != nil && (ll != nil || !isMonth) { entry.fetchedAt = now }
        cache[key] = entry
        apply(entry)
        ledgerPending = false
    }

    /// The drives after the newest finished charge, when there is one.
    private static func drivesSince(_ source: some KpiSource, end: Date?, until: Date, vehicleID: String) async -> ChartPoints? {
        guard let end, end < until else { return nil }
        return try? await source.chartPoints(since: end, until: until, vehicleID: vehicleID)
    }

    private func apply(_ e: Entry) {
        activity = e.activity
        ledger = e.ledger
        since = e.since
        ledgerPending = false
    }
}
