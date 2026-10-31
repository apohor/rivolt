import Foundation
import Observation

/// The reads behind the Money tab. Kept apart from `APIClient` so tests can fake them.
protocol MoneySource: Sendable {
    func ledgerMonth(month: String, timeZone: String, vehicleID: String?) async throws -> LedgerMonth
    func ledgerSummary(months: Int, timeZone: String, vehicleID: String?) async throws -> LedgerSummary
    func ledgerSavings(month: String) async throws -> LedgerSavings
    func chargesToConfirm() async throws -> [ConfirmCharge]
    func chargingRate() async throws -> ChargingRate
    func ledgerCSV(month: String, timeZone: String, vehicleID: String?) async throws -> Data
}

extension APIClient: MoneySource {}

/// Loads the month's statement, the summary (for the comparison and the count to confirm), the off-peak estimate and,
/// when asked, the charges to confirm. Each answer stays in memory for 15 minutes per truck and month (the ledger groups
/// every session for each call and the server rate-limits it); pull-to-refresh forces a read. A failed read, a 429
/// included, keeps what is on screen for the same truck and month; another month never shows this one's figures.
/// Months are cut in the phone's zone, so the figures tie to the web and the CSV for the same month, zone and truck.
@MainActor
@Observable
final class MoneyModel {
    static let lifetime: TimeInterval = 15 * 60
    static let summaryMonths = 24
    /// The web's one-time notice about typical fast-charging prices, hidden on this phone once dismissed.
    static let observedNoticeKey = "rivolt.money.observedNotice.v1"

    enum Which: String, CaseIterable { case last, now }

    private(set) var month: LedgerMonth?
    private(set) var summary: LedgerSummary?
    /// The previous month in full, read only while this month is in progress (the same-days comparison).
    private(set) var previous: LedgerMonth?
    private(set) var savings: LedgerSavings?
    private(set) var savingsFailed = false
    private(set) var confirm: [ConfirmCharge]?
    private(set) var homeRateSet: Bool?
    private(set) var isLoading = false
    /// The month could not be read and nothing is on screen for it.
    private(set) var error: String?
    /// A free account's limit for this month (HTTP 402 outside_free_window): shown as a note, not an error.
    private(set) var limit: ProLimit?
    /// The server has no ledger for this account (404/501: Money is off for it, or an older server).
    private(set) var unavailable = false

    private struct Entry<T> {
        let value: T
        let at: Date
    }
    @ObservationIgnored private var months: [String: Entry<LedgerMonth>] = [:]
    @ObservationIgnored private var summaries: [String: Entry<LedgerSummary>] = [:]
    @ObservationIgnored private var savingsCache: [String: Entry<LedgerSavings>] = [:]
    @ObservationIgnored private var confirmCache: Entry<[ConfirmCharge]>?
    @ObservationIgnored private var shownKey: String?

    init(month: LedgerMonth? = nil, summary: LedgerSummary? = nil, previous: LedgerMonth? = nil, savings: LedgerSavings? = nil,
         confirm: [ConfirmCharge]? = nil, homeRateSet: Bool? = nil) {
        self.month = month
        self.summary = summary
        self.previous = previous
        self.savings = savings
        self.confirm = confirm
        self.homeRateSet = homeRateSet
    }

    private static func fresh<T>(_ e: Entry<T>?, now: Date, force: Bool) -> T? {
        guard !force, let e, now.timeIntervalSince(e.at) < lifetime else { return nil }
        return e.value
    }

    private static func isMissing(_ error: Error) -> Bool {
        if case APIError.server(let status, _) = error { return status == 404 || status == 501 }
        return false
    }

    /// `vehicleID` is the truck to scope to, nil for an account with one truck (the server then covers all of them).
    func load(source: some MoneySource, vehicleID: String?, period: PeriodMonth, force: Bool = false,
              now: Date = .now, calendar: Calendar = .current) async {
        let ym = MoneyLogic.period(period, calendar: calendar)
        let tz = calendar.timeZone.identifier
        let scope = vehicleID ?? "all"
        let key = "\(scope)|\(ym)|\(tz)"
        if shownKey != key {
            month = months[key]?.value
            previous = nil
            homeRateSet = nil
            error = nil
            limit = nil
            shownKey = key
        }
        isLoading = true
        defer { isLoading = false }

        let summaryKey = "\(scope)|\(tz)"
        async let m = readMonth(source, ym, tz, vehicleID, key: key, now: now, force: force)
        async let s = readSummary(source, tz, vehicleID, key: summaryKey, now: now, force: force)
        let (mm, ss) = await (m, s)
        guard shownKey == key else { return }
        unavailable = false
        switch mm {
        case .success(let v): month = v; error = nil; limit = nil
        case .failure(let e):
            if Self.isMissing(e) { unavailable = true }
            // A 429 or a dropped connection keeps the figures already shown for this truck and month.
            if let l = ProLimit.of(e) {
                limit = l
            } else if month == nil {
                error = UserFacingError.words(e)
            }
        }
        if let ss { summary = ss }

        // The same days of the previous month, for a month in progress.
        if let cur = month, cur.isCurrentMonth {
            let pk = "\(scope)|\(MoneyLogic.shift(ym, by: -1, calendar: calendar))|\(tz)"
            if case .success(let p) = await readMonth(source, MoneyLogic.shift(ym, by: -1, calendar: calendar), tz, vehicleID, key: pk, now: now, force: force),
               shownKey == key {
                previous = p
            }
        }
        // Only an empty month needs to know whether a home rate is set (its wording).
        if let cur = month, (cur.receipts ?? []).isEmpty, cur.spend.total == 0, homeRateSet == nil {
            if let r = try? await source.chargingRate(), shownKey == key { homeRateSet = (r.homePricePerKwh ?? 0) > 0 }
        }
    }

    private func readMonth(_ source: some MoneySource, _ ym: String, _ tz: String, _ vehicleID: String?, key: String,
                           now: Date, force: Bool) async -> Result<LedgerMonth, Error> {
        if let hit = Self.fresh(months[key], now: now, force: force) { return .success(hit) }
        do {
            let v = try await source.ledgerMonth(month: ym, timeZone: tz, vehicleID: vehicleID)
            months[key] = Entry(value: v, at: now)
            return .success(v)
        } catch {
            if let kept = months[key]?.value { return .success(kept) }
            return .failure(error)
        }
    }

    private func readSummary(_ source: some MoneySource, _ tz: String, _ vehicleID: String?, key: String,
                             now: Date, force: Bool) async -> LedgerSummary? {
        if let hit = Self.fresh(summaries[key], now: now, force: force) { return hit }
        guard let v = try? await source.ledgerSummary(months: Self.summaryMonths, timeZone: tz, vehicleID: vehicleID) else {
            return summaries[key]?.value
        }
        summaries[key] = Entry(value: v, at: now)
        return v
    }

    /// The off-peak card's month: the last complete one, or this one so far.
    static func savingsPeriod(_ which: Which, now: Date, calendar: Calendar) -> String {
        let cur = MoneyLogic.period(PeriodMonth(containing: now, calendar: calendar), calendar: calendar)
        return which == .last ? MoneyLogic.shift(cur, by: -1, calendar: calendar) : cur
    }

    func loadSavings(source: some MoneySource, which: Which, force: Bool = false, now: Date = .now, calendar: Calendar = .current) async {
        let ym = Self.savingsPeriod(which, now: now, calendar: calendar)
        if let hit = Self.fresh(savingsCache[ym], now: now, force: force) {
            savings = hit
            savingsFailed = false
            return
        }
        if savingsCache[ym] == nil { savings = nil }
        do {
            let v = try await source.ledgerSavings(month: ym)
            savingsCache[ym] = Entry(value: v, at: now)
            savings = v
            savingsFailed = false
        } catch {
            savings = savingsCache[ym]?.value
            // The web leaves the card out when it has no answer.
            savingsFailed = savings == nil
        }
    }

    /// The off-peak share for a month (the share card's optional line), from the same answer as the savings card and its
    /// cache. Nil when the server has none.
    func offpeakShare(source: some MoneySource, month ym: String, now: Date = .now) async -> Double? {
        if let hit = Self.fresh(savingsCache[ym], now: now, force: false) { return hit.offpeakSharePct }
        guard let v = try? await source.ledgerSavings(month: ym) else { return savingsCache[ym]?.value.offpeakSharePct }
        savingsCache[ym] = Entry(value: v, at: now)
        return v.offpeakSharePct
    }

    /// The charges to confirm (every month): read only when that list is opened, and only while there are some.
    func loadConfirm(source: some MoneySource, force: Bool = false, now: Date = .now) async {
        if let hit = Self.fresh(confirmCache, now: now, force: force) {
            confirm = hit
            return
        }
        if let v = try? await source.chargesToConfirm() {
            confirmCache = Entry(value: v, at: now)
            confirm = v
        } else {
            confirm = confirmCache?.value ?? confirm
        }
    }

    /// The hero's lines for what is shown.
    func statement(now: Date = .now, calendar: Calendar = .current) -> MoneyStatement? {
        guard let month else { return nil }
        return MoneyStatement(month: month, months: summary?.months ?? [], previous: previous, toConfirm: summary?.toConfirmCount,
                              now: now, calendar: calendar)
    }
}
