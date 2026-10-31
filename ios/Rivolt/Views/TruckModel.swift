import Foundation
import Observation

/// The reads behind the Truck tab. Kept apart from `APIClient` so tests can fake them.
protocol TruckSource: Sendable {
    func vehicleState(id: String) async throws -> VehicleState
    func drain(vehicleID: String) async throws -> DrainSnapshot?
    func packHealth(vehicleID: String) async throws -> PackHealth
    func trends(vehicleID: String, metric: Bool) async throws -> TruckTrends?
    func coldRange(vehicleID: String, tempC: Double, grid: [Double], metric: Bool) async throws -> ColdRange?
    func leaseTerms(vehicleID: String) async throws -> LeaseTerms?
    func leaseStatus(vehicleID: String) async throws -> LeaseStatus?
    func sleepActivity(since: Date, until: Date, vehicleID: String) async throws -> [DayActivity]
    func sleepHealth(vehicleID: String) async throws -> SleepHealth?
    func chargePlaces() async throws -> [ChargePlace]
}

extension APIClient: TruckSource {}

/// One answer per card, each kept in memory for 15 minutes per truck (and month, temperature or unit where it
/// depends on one): the server works these out from every drive, charge and power-state row on each call and
/// rate-limits them. Pull-to-refresh forces a read. A failed read, a 429 included, keeps what is shown for the same
/// key; another truck or month never shows this one's. The cards fail apart: one missing leaves only its card out.
@MainActor
@Observable
final class TruckModel {
    static let lifetime: TimeInterval = 15 * 60

    /// Each card's answer: nil while not read yet; `.missing` when the server has no such endpoint, or none for this
    /// truck (no lease); `.failed` when the read failed with nothing to keep.
    enum Card<T: Equatable>: Equatable {
        case value(T)
        case missing
        case failed
        /// A free account's limit (HTTP 402 pro_required): the card says so instead of failing.
        case limited

        var value: T? { if case .value(let v) = self { return v } else { return nil } }
    }

    private(set) var state: VehicleState?
    private(set) var drain: Card<DrainSnapshot>?
    private(set) var pack: Card<PackHealth>?
    private(set) var trends: Card<TruckTrends>?
    private(set) var cold: Card<ColdRange>?
    private(set) var lease: Card<LeaseStatus>?
    private(set) var leaseTerms: LeaseTerms?
    private(set) var sleep: Card<[DayActivity]>?
    private(set) var sleepCapped = false
    /// This week's short sleeps against the truck's own 30 days (the server decides; `.missing` from an older server).
    private(set) var sleepHealth: Card<SleepHealth>?
    /// Where the account charges (all trucks, all time); held in memory only.
    private(set) var places: Card<[ChargePlace]>?
    private(set) var isLoading = false

    private struct Entry {
        let value: Any
        let at: Date
    }
    @ObservationIgnored private var cache: [String: Entry] = [:]
    @ObservationIgnored private var shownTruck: String?
    @ObservationIgnored private var shownSleep: String?
    @ObservationIgnored private var shownCold: String?

    init(state: VehicleState? = nil, drain: Card<DrainSnapshot>? = nil, pack: Card<PackHealth>? = nil, trends: Card<TruckTrends>? = nil,
         cold: Card<ColdRange>? = nil, lease: Card<LeaseStatus>? = nil, leaseTerms: LeaseTerms? = nil, sleep: Card<[DayActivity]>? = nil,
         sleepHealth: Card<SleepHealth>? = nil, places: Card<[ChargePlace]>? = nil) {
        self.state = state
        self.drain = drain
        self.pack = pack
        self.trends = trends
        self.cold = cold
        self.lease = lease
        self.leaseTerms = leaseTerms
        self.sleep = sleep
        self.sleepHealth = sleepHealth
        self.places = places
    }

    /// The cached answer for `key`, or a fresh read. A read that throws keeps the earlier answer (any age); with none,
    /// `.failed`. `nil` from the read is the server saying there is no such card.
    private func read<T: Equatable>(_ key: String, now: Date, force: Bool, _ fetch: () async throws -> T?) async -> Card<T> {
        if !force, let e = cache[key], now.timeIntervalSince(e.at) < Self.lifetime, let c = e.value as? Card<T> { return c }
        do {
            let c: Card<T> = try await fetch().map { .value($0) } ?? .missing
            cache[key] = Entry(value: c, at: now)
            return c
        } catch {
            // A free account's limit is an answer, kept like any other (it changes only when the plan does).
            if ProLimit.of(error) == .proRequired {
                cache[key] = Entry(value: Card<T>.limited, at: now)
                return .limited
            }
            return (cache[key]?.value as? Card<T>) ?? .failed
        }
    }

    /// Every card but the sleep chart and the range card (which follow the month and the temperature picked).
    func load(source: some TruckSource, vehicleID: String, metric: Bool, force: Bool = false, now: Date = .now) async {
        if shownTruck != vehicleID {
            // Another truck: never leave the last one's figures under it while this loads.
            state = nil; drain = nil; pack = nil; trends = nil; lease = nil; leaseTerms = nil; sleep = nil; sleepHealth = nil; cold = nil
            shownTruck = vehicleID
            shownSleep = nil
            shownCold = nil
        }
        isLoading = true
        defer { isLoading = false }
        let v = vehicleID
        async let st: Card<VehicleState> = read("state|\(v)", now: now, force: force) { try await source.vehicleState(id: v) }
        async let dr: Card<DrainSnapshot> = read("drain|\(v)", now: now, force: force) { try await source.drain(vehicleID: v) }
        async let pk: Card<PackHealth> = read("pack|\(v)", now: now, force: force) { try await source.packHealth(vehicleID: v) }
        async let tr: Card<TruckTrends> = read("trends|\(v)|\(metric)", now: now, force: force) { try await source.trends(vehicleID: v, metric: metric) }
        async let lt: Card<LeaseTerms> = read("lease|\(v)", now: now, force: force) { try await source.leaseTerms(vehicleID: v) }
        async let sh: Card<SleepHealth> = read("sleepHealth|\(v)", now: now, force: force) { try await source.sleepHealth(vehicleID: v) }
        // The account's places, not the truck's: one key, so switching trucks doesn't read them again.
        async let pl: Card<[ChargePlace]> = read("places", now: now, force: force) { try await source.chargePlaces() }
        let (s, d, p, t, l, c, h) = await (st, dr, pk, tr, lt, pl, sh)
        guard shownTruck == v else { return }
        places = c
        sleepHealth = h
        if let sv = s.value { state = sv }
        drain = d
        pack = p
        trends = t
        leaseTerms = l.value
        // The status only for a truck with terms (as the web).
        if l.value != nil {
            let ls: Card<LeaseStatus> = await read("leaseStatus|\(v)", now: now, force: force) { try await source.leaseStatus(vehicleID: v) }
            if shownTruck == v { lease = ls }
        } else {
            lease = .missing
        }
    }

    /// The idle-awake chart for the month picked: UTC days, at most the last 90, through today.
    func loadSleep(source: some TruckSource, vehicleID: String, month: PeriodMonth, force: Bool = false, now: Date = .now,
                   calendar: Calendar = .current) async {
        let w = TruckLogic.sleepWindow(month, now: now, calendar: calendar)
        let key = "sleep|\(vehicleID)|\(w.since.timeIntervalSince1970)|\(w.until.timeIntervalSince1970)"
        if shownSleep != key {
            sleep = (cache[key]?.value as? Card<[DayActivity]>)
            shownSleep = key
        }
        let c: Card<[DayActivity]> = await read(key, now: now, force: force) { try await source.sleepActivity(since: w.since, until: w.until, vehicleID: vehicleID) }
        guard shownSleep == key else { return }
        sleep = c
        sleepCapped = w.capped
    }

    /// The range near `tempC`, with the grid so the answer says which chips have history.
    func loadCold(source: some TruckSource, vehicleID: String, tempC: Double, fahrenheit: Bool, metric: Bool, force: Bool = false,
                  now: Date = .now) async {
        let grid = TruckLogic.grid(fahrenheit: fahrenheit)
        let key = "cold|\(vehicleID)|\(tempC)|\(fahrenheit)|\(metric)"
        if shownCold != key {
            if let hit = cache[key]?.value as? Card<ColdRange> { cold = hit }
            shownCold = key
        }
        let c: Card<ColdRange> = await read(key, now: now, force: force) {
            try await source.coldRange(vehicleID: vehicleID, tempC: tempC, grid: grid, metric: metric)
        }
        guard shownCold == key else { return }
        cold = c
    }
}
