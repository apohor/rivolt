import Foundation

/// Thin URLSession wrapper around the Rivolt backend. No third-party
/// HTTP client — the surface is small enough that URLSession + a
/// single `request` helper beats adopting Alamofire / Moya, and we
/// avoid a dependency that would need to be resolved via SPM before
/// the project can even open in Xcode.
///
/// Requests carry the server's app session token as `Authorization:
/// Bearer`, read from the per-server `TokenStore` (the Keychain), never
/// from a cookie jar: a token is only ever sent to the server that issued it.
actor APIClient {
    private var baseURL: URL
    private let tokens: TokenStore
    private let session: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder
    /// The demo truck's client (see `DemoMode`): reads only, and a 401 or 403 means "not part of the demo".
    private let demo: Bool

    /// `session` is for tests (a stubbed URLProtocol); the app uses the default.
    init(baseURL: URL, tokens: TokenStore, session: URLSession? = nil, demo: Bool = false) {
        self.baseURL = baseURL
        self.tokens = tokens
        self.demo = demo
        let config = URLSessionConfiguration.ephemeral
        // No cookies: the app authenticates with its bearer token only.
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        if demo {
            // Nothing is shared or kept: no credentials, no cached answers.
            config.urlCredentialStorage = nil
            config.urlCache = nil
            config.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        }
        // Timeouts matter on cellular: the default (60s) leaves a
        // frozen-looking UI for a full minute when a self-hosted server is
        // unreachable. 15s is plenty for the JSON surface.
        config.timeoutIntervalForRequest = 15
        config.timeoutIntervalForResource = 30
        self.session = session ?? URLSession(configuration: config)

        self.decoder = JSON.decoder()

        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        self.encoder = encoder
    }

    func updateBaseURL(_ url: URL) {
        baseURL = url
    }

    // MARK: - Endpoints

    /// Whether this server is Rivolt and accepts app sign-in. Unauthenticated.
    func appConfig() async throws -> AppConfig {
        try await request("GET", "/api/app/config", authenticated: false)
    }

    private struct AppTokenBody: Encodable {
        let code: String
        let codeVerifier: String
        let deviceName: String
    }

    /// Exchanges the one-time code from the browser and the PKCE verifier for
    /// an app session token. Unauthenticated: no stored token is sent.
    func appToken(code: String, verifier: String, deviceName: String) async throws -> AppToken {
        try await request("POST", "/api/auth/app/token",
                          body: AppTokenBody(code: code, codeVerifier: verifier, deviceName: deviceName),
                          authenticated: false)
    }

    func logout() async throws {
        let _: Empty = try await request("POST", "/api/auth/logout")
    }

    func me() async throws -> AuthUser {
        try await request("GET", "/api/auth/me")
    }

    // MARK: Units

    /// How the account shows distance and temperature.
    func accountUnits() async throws -> AccountUnits {
        try await request("GET", "/api/settings/units")
    }

    /// A pick (or the one first-time seed from the phone's region); the server answers with the units now in force.
    func setUnits(_ patch: UnitsPatch) async throws -> AccountUnits {
        try await request("PATCH", "/api/settings/units", body: patch)
    }

    // MARK: Signed-in devices

    /// The browsers and iPhones signed in as this person (newest first as the server sends them).
    /// With `recent`, the list is cut to every iPhone sign-in, this one and the newest N other browsers, and `hiddenWeb` counts the
    /// rest (the screen says so and offers the whole list). Without it, everything.
    /// The passkeys on the account (a read; adding and removing are on the web).
    func passkeys() async throws -> PasskeyList {
        try await request("GET", "/api/auth/passkeys")
    }

    func signedInDevices(recent: Int? = nil) async throws -> SignedInDevices {
        try await request("GET", "/api/auth/sessions" + (recent.map { "?recent=\($0)" } ?? ""))
    }

    /// Signs one device out. The server allows the app this one call, for this person's own devices only; there is no
    /// "sign out everywhere else" on the phone (a stolen phone could lock the owner out).
    func signOutDevice(id: String) async throws {
        let _: Empty = try await request("DELETE", "/api/auth/sessions/\(pathID(id))")
    }

    // MARK: Push (APNs)

    private struct ApnsRegisterBody: Encodable {
        let token: String
        let env: String
    }

    private struct ApnsRegistered: Decodable {
        let preferences: PushPreferences
    }

    private struct ApnsTokenBody: Encodable {
        let token: String
    }

    /// Tells the server this phone's Apple device token. The server answers with
    /// the phone's switches (new phones start with the web's defaults).
    func registerApns(token: String, environment: String) async throws -> PushPreferences {
        let r: ApnsRegistered = try await request("POST", "/api/push/apns/register", body: ApnsRegisterBody(token: token, env: environment))
        return r.preferences
    }

    /// Changes one switch of this phone (`on_charging_done` and so on).
    func setApnsPreference(token: String, wireKey: String, on: Bool) async throws {
        let body: [String: AnyJSON] = ["token": .string(token), wireKey: .bool(on)]
        let _: Empty = try await request("POST", "/api/push/apns/preferences", body: body)
    }

    private struct ApnsTestSent: Decodable {
        let sent: Int
    }

    /// Asks the server to send a fixed test notification to this person's own iPhones; the answer is
    /// how many it sent to. 404 when APNs is off or no iPhone is registered; the server rate-limits it.
    func testApns() async throws -> Int {
        let r: ApnsTestSent = try await request("POST", "/api/push/apns/test")
        return r.sent
    }

    private struct ActivityRegisterBody: Encodable {
        let token: String
        let activityToken: String
        let vehicleId: String
    }

    private struct ActivityEndBody: Encodable {
        let activityToken: String
    }

    /// Tells the server this charging Live Activity's push token (the app started it; v1 has no push-to-start).
    /// `deviceToken` is this iPhone's APNs token, `activityToken` ActivityKit's per-activity token (hex).
    func registerActivity(deviceToken: String, activityToken: String, vehicleID: String) async throws {
        let _: Empty = try await request("POST", "/api/push/apns/activity", body: ActivityRegisterBody(token: deviceToken, activityToken: activityToken, vehicleId: vehicleID))
    }

    /// Asks the server to send fixed test figures (50%, 7.2 kW, 60 min), then an end, to this person's running
    /// Live Activities. 404: Live Activities are off on the server or nothing is registered; 409/429 as usual.
    func testLiveActivity() async throws {
        let _: Empty = try await request("POST", "/api/push/apns/activity/test")
    }

    func endActivity(activityToken: String) async throws {
        let _: Empty = try await request("POST", "/api/push/apns/activity/end", body: ActivityEndBody(activityToken: activityToken))
    }

    func unregisterApns(token: String) async throws {
        let _: Empty = try await request("POST", "/api/push/apns/unregister", body: ApnsTokenBody(token: token))
    }

    // MARK: Milestones and the "Three things" card (the same reads the web's Now page makes)

    private struct MilestoneDismissBody: Encodable {
        let vehicleId: String
        let key: String
        private enum CodingKeys: String, CodingKey { case vehicleId = "vehicle_id", key }
    }

    /// Newest first, account-wide, from the last 30 days.
    func milestones() async throws -> [Milestone] {
        let r: MilestonesResponse = try await request("GET", "/api/milestones")
        return r.milestones
    }

    func dismissMilestone(vehicleID: String, key: String) async throws {
        let _: Empty = try await request("POST", "/api/milestones/dismiss", body: MilestoneDismissBody(vehicleId: vehicleID, key: key))
    }

    private struct AnyBody: Decodable {}

    /// Whether the truck has lease terms: 404 is the endpoint's answer for "none", and so is 501 on a server without it.
    func hasLease(vehicleID: String) async throws -> Bool {
        do {
            let _: AnyBody = try await request("GET", "/api/vehicles/\(pathID(vehicleID))/lease")
            return true
        } catch APIError.server(let status, _) where status == 404 || status == 501 {
            return false
        }
    }

    func statementEmail() async throws -> StatementEmailSetting {
        try await request("GET", "/api/settings/statement-email")
    }

    // MARK: Now detail reads (the same ones the web's Now page makes)

    private func pathID(_ id: String) -> String {
        id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
    }

    /// A query value: unlike a path component, `&`, `=`, `+` and `#` must be escaped too.
    private func queryValue(_ value: String) -> String {
        var allowed = CharacterSet.urlQueryAllowed
        allowed.remove(charactersIn: "&=+#")
        return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
    }

    /// The charge in progress; nil when the server has none (404) or doesn't know the endpoint.
    func liveSession(id: String) async throws -> LiveSession? {
        do { return try await request("GET", "/api/live-session/\(pathID(id))") } catch APIError.server(404, _) { return nil }
    }

    /// The drive in progress; 204 (or 404 from an older server) means none is open yet.
    func liveDrive(id: String) async throws -> LiveDrive? {
        do {
            let (data, http) = try await send("GET", "/api/live-drive/\(pathID(id))")
            if http.statusCode == 204 || data.isEmpty { return nil }
            do { return try decoder.decode(LiveDrive.self, from: data) } catch { throw APIError.decoding(error) }
        } catch APIError.server(404, _) { return nil }
    }

    /// The newest charge (for its place and cost).
    func latestCharge(vehicleID: String) async throws -> ChargeBrief? {
        try await recentCharges(vehicleID: vehicleID).first
    }

    /// The newest few charges, newest first: while one is in progress the newest row is that one, and "since the last
    /// charge" needs the one before.
    func recentCharges(vehicleID: String) async throws -> [ChargeBrief] {
        try await request("GET", "/api/charges?limit=3&vehicle_id=\(queryValue(vehicleID))")
    }

    /// Asks for a few: the server drops a gear-bounce row after picking it, so a one-row page can come back empty.
    func latestDrive(vehicleID: String) async throws -> DriveBrief? {
        let rows: [DriveBrief] = try await request("GET", "/api/drives?limit=5&vehicle_id=\(queryValue(vehicleID))")
        return rows.first
    }

    // MARK: Chart reads (the same ones the web's Home charts use, without coordinates)

    private func rfc3339(_ date: Date) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f.string(from: date)
    }

    /// Drives and charges in `[since, until)` for one truck, newest first, no coordinates. The server may answer 429.
    func activity(since: Date, until: Date, vehicleID: String) async throws -> ActivityResponse {
        try await request("GET", "/api/activity?since=\(queryValue(rfc3339(since)))&until=\(queryValue(rfc3339(until)))&vehicle_id=\(queryValue(vehicleID))")
    }

    /// The month's charging spend, as Money shows it. `vehicleID` is only sent for an account with more than one truck.
    func ledgerSpend(month: String, timeZone: String, vehicleID: String?) async throws -> LedgerMonthSpend {
        var path = "/api/ledger/monthly?month=\(queryValue(month))&tz=\(queryValue(timeZone))"
        if let vehicleID { path += "&vehicle_id=\(queryValue(vehicleID))" }
        return try await request("GET", path)
    }

    /// Home, Away and Fast counts and energy in `[since, until)`. The server may answer 429 (a per-user limit).
    func chargeSplit(since: Date, until: Date, vehicleID: String) async throws -> ChargeSplit {
        try await request("GET", "/api/charge-split?since=\(queryValue(rfc3339(since)))&until=\(queryValue(rfc3339(until)))&vehicle_id=\(queryValue(vehicleID))")
    }

    /// Drives and charges in `[since, until)`: distances and charge levels only.
    func chartPoints(since: Date, until: Date, vehicleID: String) async throws -> ChartPoints {
        try await request("GET", "/api/chart-points?since=\(queryValue(rfc3339(since)))&until=\(queryValue(rfc3339(until)))&vehicle_id=\(queryValue(vehicleID))")
    }

    /// Per-day power-state hours (UTC days) in `[since, until)`; the server's key here is `vehicle`, not `vehicle_id`.
    func sleepActivity(since: Date, until: Date, vehicleID: String) async throws -> [DayActivity] {
        try await request("GET", "/api/sleep-activity?since=\(queryValue(rfc3339(since)))&until=\(queryValue(rfc3339(until)))&vehicle=\(queryValue(vehicleID))")
    }

    // MARK: Money (the web's Money page reads: the ledger, the off-peak estimate and the charges to confirm)

    /// `vehicleID` is only sent for an account with more than one truck; without it the server covers all of them.
    private func ledgerQuery(_ items: [(String, String?)]) -> String {
        items.compactMap { k, v in v.map { "\(k)=\(queryValue($0))" } }.joined(separator: "&")
    }

    /// One month's statement, cut in `timeZone`. The server may answer 429 (a per-user limit).
    func ledgerMonth(month: String, timeZone: String, vehicleID: String?) async throws -> LedgerMonth {
        try await request("GET", "/api/ledger/monthly?" + ledgerQuery([("month", month), ("tz", timeZone), ("vehicle_id", vehicleID)]))
    }

    /// The last `months` months, oldest first, and the count of charges to confirm.
    func ledgerSummary(months: Int, timeZone: String, vehicleID: String?) async throws -> LedgerSummary {
        try await request("GET", "/api/ledger/summary?" + ledgerQuery([("months", String(months)), ("tz", timeZone), ("vehicle_id", vehicleID)]))
    }

    /// One truck's efficiency breakdowns for `[since, until)` in `units` ("imperial" or "metric"), as the web's
    /// Activity → Efficiency. nil when the server has no such endpoint (404).
    func efficiency(vehicleID: String, units: String, since: Date, until: Date) async throws -> EfficiencyInsights? {
        do {
            let insights: EfficiencyInsights = try await request("GET", "/api/insights/efficiency?vehicle=\(queryValue(vehicleID))&units=\(queryValue(units))&since=\(queryValue(rfc3339(since)))&until=\(queryValue(rfc3339(until)))")
            return insights
        } catch APIError.server(let status, _) where status == 404 {
            return nil
        }
    }

    /// The off-peak estimate for a month. Like the web, no zone or truck is sent: the server cuts the month in the zone
    /// the off-peak schedule is read in.
    func ledgerSavings(month: String) async throws -> LedgerSavings {
        try await request("GET", "/api/ledger/savings?" + ledgerQuery([("month", month)]))
    }

    /// Every charge whose auto-detected network is still open (estimated or needing confirmation), newest first, from
    /// the server's coordinate-free list.
    func chargesToConfirm() async throws -> [ConfirmCharge] {
        let rows: [ConfirmCharge] = try await request("GET", "/api/charges/to-confirm")
        return rows.filter { MoneyLogic.isOpenNetworkState($0.networkState) }.sorted { $0.startedAt > $1.startedAt }
    }

    /// The month's statement as the web's CSV download (the same numbers as `ledgerMonth`): totals, then one line per
    /// session with its place (home or away), kind, cost and network. No coordinates. Asked for only when the owner
    /// exports it.
    func ledgerCSV(month: String, timeZone: String, vehicleID: String?) async throws -> Data {
        let (data, _) = try await send("GET", "/api/ledger/monthly.csv?" + ledgerQuery([("month", month), ("tz", timeZone), ("vehicle_id", vehicleID)]))
        return data
    }

    func chargingRate() async throws -> ChargingRate {
        try await request("GET", "/api/settings/charging/")
    }

    // MARK: Truck (the web's Truck page reads; none carries a coordinate)

    /// Nil when the server has no such endpoint (404/501): the card is left out, as on the web.
    private func optional<T: Decodable>(_ path: String) async throws -> T? {
        do { return try await request("GET", path) } catch APIError.server(let status, _) where status == 404 || status == 501 { return nil }
    }

    /// Battery lost while parked, over 7 and 30 days and per day. Like the web, no zone is sent: the server's days are
    /// UTC days, as the sleep chart's are.
    func drain(vehicleID: String) async throws -> DrainSnapshot? {
        try await optional("/api/vehicles/\(pathID(vehicleID))/drain")
    }

    /// This week's short sleeps against the truck's own last 30 days; nil from an older server without the endpoint.
    func sleepHealth(vehicleID: String) async throws -> SleepHealth? {
        try await optional("/api/vehicles/\(pathID(vehicleID))/sleep-health")
    }

    /// The headline (over every sample) and the newest 30 samples, all the sparkline draws. An older server ignores
    /// `limit` and sends every sample.
    func packHealth(vehicleID: String) async throws -> PackHealth {
        try await request("GET", "/api/vehicles/\(pathID(vehicleID))/pack-health?limit=30")
    }

    /// `metric` picks km and kWh/100 km; otherwise miles and mi/kWh.
    func trends(vehicleID: String, metric: Bool) async throws -> TruckTrends? {
        try await optional("/api/vehicles/\(pathID(vehicleID))/trends?units=\(metric ? "metric" : "imperial")")
    }

    /// The owner's range near `tempC`; `grid` (°C) is the five temperatures offered, so the answer says which have
    /// enough history.
    func coldRange(vehicleID: String, tempC: Double, grid: [Double], metric: Bool) async throws -> ColdRange? {
        let g = grid.map { String($0) }.joined(separator: ",")
        return try await optional("/api/vehicles/\(pathID(vehicleID))/cold-range?temp_c=\(tempC)&grid=\(queryValue(g))&units=\(metric ? "metric" : "imperial")")
    }

    /// The lease terms; nil when none are set (404).
    func leaseTerms(vehicleID: String) async throws -> LeaseTerms? {
        try await optional("/api/vehicles/\(pathID(vehicleID))/lease")
    }

    func leaseStatus(vehicleID: String) async throws -> LeaseStatus? {
        try await optional("/api/vehicles/\(pathID(vehicleID))/lease/status")
    }

    // MARK: Maps

    /// Where the account charges (all trucks, all time), as the server clusters it: labels, centres and totals.
    func chargePlaces() async throws -> [ChargePlace] {
        try await request("GET", "/api/charges/clusters")
    }

    /// One drive's route (its recorded trace and ends), for the map on its screen. Held in memory only.
    func driveRoute(id: String) async throws -> DriveRoute {
        try await request("GET", "/api/drives/\(pathID(id))")
    }

    /// Every drive of a truck with its stored route, for the Activity map (the web's Activity map reads the same
    /// list). Rows are narrowed to `MapDrive`.
    func mapDrives(vehicleID: String) async throws -> [MapDrive] {
        try await request("GET", "/api/drives?limit=5000&vehicle_id=\(queryValue(vehicleID))")
    }

    /// Every charge of a truck, for the Activity map's dots. Rows are narrowed to `MapCharge`.
    func mapCharges(vehicleID: String) async throws -> [MapCharge] {
        try await request("GET", "/api/charges?limit=5000&vehicle_id=\(queryValue(vehicleID))")
    }

    /// One charge's row, read once on its screen: where it happened and the numbers on its session card (ChargeRow).
    func chargeRow(id: String) async throws -> ChargeRow {
        try await request("GET", "/api/charges/\(pathID(id))")
    }

    /// The samples of `[since, until)` for one truck (the web's drive-map window: the drive and 10 minutes either side,
    /// at most 2000, downsampled by the server beyond that).
    func driveSamples(since: Date, until: Date, vehicleID: String, limit: Int = 2000) async throws -> [DriveSample] {
        try await request("GET", "/api/samples?since=\(queryValue(rfc3339(since)))&until=\(queryValue(rfc3339(until)))&limit=\(limit)&vehicle=\(queryValue(vehicleID))")
    }

    /// The samples of one charge's window for the charging curve: the charge and a minute either side, at most 2000
    /// (the server downsamples an explicit window beyond that), the truck's own.
    func chargeSamples(since: Date, until: Date, vehicleID: String, limit: Int = 2000) async throws -> [ChargeSample] {
        try await request("GET", "/api/samples?since=\(queryValue(rfc3339(since)))&until=\(queryValue(rfc3339(until)))&limit=\(limit)&vehicle=\(queryValue(vehicleID))")
    }

    // MARK: Charging rates

    /// The home electricity rate and its currency.
    func chargingRates() async throws -> ChargingRates {
        try await request("GET", "/api/settings/charging")
    }

    /// Changes the home rate: only the fields given are sent, and the answer is the rate as the server now has it. The
    /// app's one settings write (the server refuses any other from the app; a 403 means this server doesn't allow it).
    func setChargingRates(pricePerKwh: Double?, currency: String?) async throws -> ChargingRates {
        try await request("PATCH", "/api/settings/charging", body: ChargingRatesPatch(homePricePerKwh: pricePerKwh, homeCurrency: currency))
    }

    /// Changes the home rate and the planner's gas inputs: only the fields set are sent; the answer is the settings as the server now has them.
    func setChargingRates(_ patch: ChargingRatesPatch) async throws -> ChargingRates {
        try await request("PATCH", "/api/settings/charging", body: patch)
    }

    // MARK: Off-peak, networks and planner defaults

    func offpeak() async throws -> OffpeakSchedule {
        try await request("GET", "/api/settings/charging/offpeak")
    }

    /// Replaces the schedule; the answer is the schedule as saved.
    func setOffpeak(_ body: OffpeakPut) async throws -> OffpeakSchedule {
        try await request("PUT", "/api/settings/charging/offpeak", body: body)
    }

    func setFlatRate(_ on: Bool) async throws -> FlatRateBody {
        try await request("PUT", "/api/settings/charging/flat-rate", body: FlatRateBody(flatRate: on))
    }

    func chargingNetworks() async throws -> [ChargingNetwork] {
        try await request("GET", "/api/settings/charging/networks")
    }

    /// Replaces the price book; the answer is the list as saved.
    func setChargingNetworks(_ rows: [ChargingNetwork]) async throws -> [ChargingNetwork] {
        try await request("PUT", "/api/settings/charging/networks", body: rows)
    }

    func resetChargingNetworks() async throws -> [ChargingNetwork] {
        try await request("POST", "/api/settings/charging/networks/reset")
    }

    func membershipTerms() async throws -> [MembershipTerm] {
        let r: MembershipTermsResponse = try await request("GET", "/api/settings/charging/membership-terms")
        return r.terms ?? []
    }

    func plannerPrefs() async throws -> PlannerPrefs {
        try await request("GET", "/api/settings/planner")
    }

    func setPlannerPrefs(_ prefs: PlannerPrefs) async throws -> PlannerPrefs {
        try await request("PUT", "/api/settings/planner", body: prefs)
    }

    // MARK: Trip planner

    /// A plan for one outing. The server passes Rivian's planner through, so this can answer 401 with a `class` (Rivian needs
    /// reconnecting: `APIError.rivianReauth`), 429 (its hourly planning quota, or the server's) or a 502 on a long day.
    func planTrip(_ body: TripPlanRequest) async throws -> TripPlan {
        try await request("POST", "/api/trips/plan", body: body)
    }

    /// The sample trips the demo offers (static, read only).
    func demoTrips() async throws -> DemoTrips {
        try await request("GET", "/api/demo/trips")
    }

    /// A sample trip's stored plan, as the planner answered it (a one-day plan, or a two-day one with `kind: "multiday"`).
    func demoTripPlan(id: String) async throws -> JSONValue {
        try await request("GET", "/api/demo/trips/\(pathID(id))/plan")
    }

    /// The DC fast chargers within a band either side of a route (the server's own charger listing).
    func chargersAlong(_ body: ChargersAlongRequest) async throws -> ChargersAlong {
        try await request("POST", "/api/maps/chargers-along", body: body)
    }

    /// The trip as days with a night between: each day planned from where the last ended.
    func planMultiday(_ body: TripMultidayRequest) async throws -> TripMultiday {
        try await request("POST", "/api/trips/plan-multiday", body: body)
    }

    /// Hotels and campgrounds with a listed charger near where a day's driving budget runs out.
    func overnightCandidates(_ body: OvernightCandidatesRequest) async throws -> OvernightCandidates {
        try await request("POST", "/api/trips/overnight-candidates", body: body)
    }

    /// Places matching a typed name, for an origin or a destination (the server's own geocoder).
    func geocode(_ q: String, count: Int = 5) async throws -> [PlaceMatch] {
        try await request("GET", "/api/geocode?q=\(queryValue(q))&count=\(count)")
    }

    func savedTrips() async throws -> [SavedTrip] {
        try await request("GET", "/api/trips/saved")
    }

    /// Saves a trip under a name (409 when the name is taken). `inputs` and `plan` are written as the web writes them, so each reads the other's.
    func saveTrip(_ body: SavedTripBody) async throws -> SavedTrip {
        try await request("POST", "/api/trips/saved", body: body)
    }

    func updateSavedTrip(id: String, _ body: SavedTripBody) async throws -> SavedTrip {
        try await request("PUT", "/api/trips/saved/\(pathID(id))", body: body)
    }

    /// A saved trip against the outing that was driven (one comparison, or one per day).
    func planVsActual(savedID: String) async throws -> PlanVsActualAnswer {
        try await request("GET", "/api/trips/saved/\(pathID(savedID))/actual")
    }

    /// Says a drive was this saved trip (`day` for a multi-day trip's day).
    func linkPlanVsActual(savedID: String, _ body: PlanVsActualLinkBody) async throws {
        let _: PlanVsActual = try await request("POST", "/api/trips/saved/\(pathID(savedID))/link", body: body)
    }

    /// Says a suggested or linked drive was not this saved trip.
    func unlinkPlanVsActual(savedID: String, _ body: PlanVsActualLinkBody) async throws {
        let _: PlanVsActual = try await request("POST", "/api/trips/saved/\(pathID(savedID))/unlink", body: body)
    }

    // MARK: Essential settings

    func homeLocation() async throws -> HomeLocation {
        try await request("GET", "/api/settings/home-location")
    }

    /// Sets the home (or, with `HomeLocationBody.clear`, removes it); the answer is the home as the server now has it. The server
    /// limits this to 10 a minute (429).
    func setHomeLocation(_ body: HomeLocationBody) async throws -> HomeLocation {
        try await request("PUT", "/api/settings/home-location", body: body)
    }

    /// Back to the home the last change replaced; 404 when there is none.
    func restoreHomeLocation() async throws -> HomeLocation {
        try await request("POST", "/api/settings/home-location/restore")
    }

    /// A street address on the server's own geocoder (the address goes in the body, never the URL). 404 without one.
    func searchHomeAddress(_ q: String) async throws -> [PlaceMatch] {
        try await request("POST", "/api/settings/home-location/search", body: HomeSearchBody(q: q))
    }

    func vehicleProfile(id: String) async throws -> VehicleProfile {
        try await request("GET", "/api/vehicles/\(pathID(id))/profile")
    }

    func saveVehicleProfile(id: String, _ profile: VehicleProfile) async throws -> VehicleProfile {
        try await request("PUT", "/api/vehicles/\(pathID(id))/profile", body: profile)
    }

    func truckPosition(id: String) async throws -> TruckPosition {
        try await request("GET", "/api/state/\(pathID(id))")
    }

    /// Valhalla's map matching through our proxy (in-cluster on prod).
    func valhallaTrace(_ body: ValhallaTraceRequest) async throws -> ValhallaTraceResponse {
        try await request("POST", "/api/maps/valhalla/trace_route", body: body)
    }

    /// Valhalla's routing between two points through our proxy.
    func valhallaRoute(_ body: ValhallaRouteRequest) async throws -> ValhallaRouteResponse {
        try await request("POST", "/api/maps/valhalla/route", body: body)
    }

    func vehicles() async throws -> [Vehicle] {
        try await request("GET", "/api/vehicles")
    }

    func vehicleState(id: String) async throws -> VehicleState {
        // Path components with slashes aren't expected here (Rivian
        // vehicle IDs are UUIDs), but addingPercentEncoding is cheap
        // insurance against ever passing one that does.
        let escaped = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        return try await request("GET", "/api/state/\(escaped)")
    }

    /// The server's view of the truck right now (now-contract v1): state,
    /// freshness inputs and the current charge. Nil on a server without it
    /// (404 or 501), so the card falls back to `vehicleState` alone.
    /// A fresh link to a software release's notes (`which` is "current" or "available"). Rivian's own link is a signed
    /// URL that expires after an hour, so the server fetches one when this is asked and never stores it; it goes straight
    /// to the browser and is not kept or logged here.
    func otaNotesURL(vehicleID: String, which: String) async throws -> URL {
        struct Answer: Decodable { let url: String }
        let escaped = vehicleID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? vehicleID
        let answer: Answer = try await request("GET", "/api/vehicles/\(escaped)/ota/notes?which=\(which)")
        guard let url = TruckLogic.notesURL(answer.url) else { throw APIError.badURL }
        return url
    }

    func vehicleNow(id: String) async throws -> NowSnapshot? {
        let escaped = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? id
        do {
            return try await request("GET", "/api/vehicles/\(escaped)/now")
        } catch APIError.server(404, _), APIError.server(501, _) {
            return nil
        }
    }

    // MARK: - Core

    private struct Empty: Decodable {}

    private func request<Response: Decodable>(
        _ method: String,
        _ path: String,
        body: (any Encodable)? = nil,
        authenticated: Bool = true
    ) async throws -> Response {
        let (data, _) = try await send(method, path, body: body, authenticated: authenticated)
        if Response.self == Empty.self {
            // swiftlint:disable:next force_cast
            return Empty() as! Response
        }
        do {
            return try decoder.decode(Response.self, from: data)
        } catch {
            throw APIError.decoding(error)
        }
    }

    /// One round trip: the body and response of a 2xx answer, or the error for anything else.
    private func send(
        _ method: String,
        _ path: String,
        body: (any Encodable)? = nil,
        authenticated: Bool = true
    ) async throws -> (Data, HTTPURLResponse) {
        if demo, method != "GET", method != "HEAD" {
            throw APIError.transport("The demo truck is read-only.")
        }
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw APIError.badURL
        }
        var req = URLRequest(url: url)
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if authenticated, let token = try tokens.token(for: baseURL) {
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try encoder.encode(AnyEncodable(body))
        }
        let (data, resp) = try await session.data(for: req)
        guard let http = resp as? HTTPURLResponse else {
            throw APIError.transport("not an HTTP response")
        }
        guard (200..<300).contains(http.statusCode) else {
            // In the demo, a path the sample account isn't served on answers 401: to the screens that is "not here" (a missing card),
            // never "your sign-in has ended".
            if demo, http.statusCode == 401 || http.statusCode == 403 {
                throw APIError.server(status: 404, message: "Not part of the demo.")
            }
            // Surface the server's error body verbatim when possible.
            // Rivolt returns either JSON `{"error": "..."}` or plain
            // text from http.Error; try the former first.
            let message: String
            let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
            // A 401 that names a `class` is Rivian's, not this session's: the person has to reconnect their Rivian (on the web), and the
            // phone's own sign-in is fine. Only a bare 401 means the session is dead.
            // The planner's hourly allowance (Rivian's quota, per account): 429 with that class and how long until it refills.
            if http.statusCode == 429, let json, json["class"] as? String == "plan_quota" {
                let after = (json["retry_after_sec"] as? Double).map { Int($0) } ?? Int(http.value(forHTTPHeaderField: "Retry-After") ?? "")
                throw APIError.planQuota(retryAfterSeconds: after)
            }
            if http.statusCode == 401, let json, json["class"] is String {
                throw APIError.rivianReauth
            }
            if let j = json, let e = j["error"] as? String, !e.isEmpty {
                message = e
            } else {
                message = String(data: data, encoding: .utf8) ?? "HTTP \(http.statusCode)"
            }
            throw APIError.server(status: http.statusCode, message: message)
        }
        return (data, http)
    }
}

enum APIError: LocalizedError {
    case badURL
    case transport(String)
    case server(status: Int, message: String)
    /// A 401 whose body names an upstream `class`: Rivian needs reconnecting. Not a dead Rivolt session.
    case rivianReauth
    /// 429 with the class `plan_quota`: this account has used the planner's allowance for the hour.
    case planQuota(retryAfterSeconds: Int?)
    case decoding(Error)

    var errorDescription: String? {
        switch self {
        case .badURL: return "Invalid server URL."
        case .transport(let m): return m
        case .server(_, let m): return m
        case .rivianReauth: return "Rivian needs to be reconnected."
        case .planQuota: return "The planner's hourly allowance is used."
        case .decoding(let e): return "Could not parse response: \(e.localizedDescription)"
        }
    }
}

/// AnyEncodable is the standard trick to let `request` accept any
/// Encodable body without leaking a generic parameter onto the
/// function signature (which would complicate the call site).
private struct AnyEncodable: Encodable {
    private let wrapped: any Encodable
    init(_ wrapped: any Encodable) {
        self.wrapped = wrapped
    }
    func encode(to encoder: Encoder) throws {
        try wrapped.encode(to: encoder)
    }
}

/// A JSON value for a body whose keys are chosen at run time (the one switch
/// being changed). The keys are already snake_case, which the encoder's key
/// strategy leaves as it is.
enum AnyJSON: Encodable {
    case string(String)
    case bool(Bool)

    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .string(let s): try c.encode(s)
        case .bool(let b): try c.encode(b)
        }
    }
}

extension APIClient: ChartsSource {}
extension APIClient: ChargeSamplesSource {}
extension APIClient: ChargeRowSource {}
extension APIClient: ChargingRatesSource {}
extension APIClient: ChargingSettingsSource {}
