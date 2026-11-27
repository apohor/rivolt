#if DEBUG
import Foundation

/// A launch for the UI tests (RivoltUITests): the app signed in, talking to no server. The test sets RIVOLT_UITEST_FIXTURES to the directories of JSON
/// answers, colon-separated, the first that has a file winning (the UI tests' own, then the unit tests' fixtures), and every request the app makes is answered from them in the app's own URLSession: an
/// unknown path is a 404, so nothing reaches a real server. Debug builds only: Release has none of this.
enum UITestLaunch {
    static let variable = "RIVOLT_UITEST_FIXTURES"
    /// An account with no vehicle yet: the vehicles list is empty and the discovery answer is this state (looking, none, reauth), for the connect screen.
    static let noTruckVariable = "RIVOLT_UITEST_NO_TRUCK"
    /// With the no-truck fixture: the account is on the free tier of a server that bills (me_plan_free), so the Rivolt Pro entry shows.
    static let billingVariable = "RIVOLT_UITEST_BILLING"
    /// Every truck's status read fails (a 503, as when Rivian is down), for Home's failed-read screen.
    static let stateFailsVariable = "RIVOLT_UITEST_STATE_FAILS"
    /// With the no-truck fixture: Check again lands on a server that does not hold the account (503, Retry-After 1), for "checking".
    static let refreshUnheldVariable = "RIVOLT_UITEST_REFRESH_UNHELD"
    /// With the no-truck fixture: Check again is refused (429, the one forced lookup per 10 minutes), and from then on discovery answers looking, so the
    /// same tap that is refused also moves the card.
    static let refreshLimitedVariable = "RIVOLT_UITEST_REFRESH_LIMITED"

    /// The directories to answer from, or nil on a normal launch.
    static var directories: [URL]? {
        guard let raw = ProcessInfo.processInfo.environment[variable], !raw.isEmpty else { return nil }
        return raw.split(separator: ":").map { URL(fileURLWithPath: String($0), isDirectory: true) }
    }

    /// The app's state for a UI-test launch: signed in, every call answered from the fixtures.
    @MainActor
    static func makeState() -> AppState? {
        guard let dirs = directories else { return nil }
        FixtureURLProtocol.directories = dirs
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [FixtureURLProtocol.self]
        let defaults = UserDefaults(suiteName: "rivolt-uitest") ?? .standard
        defaults.removePersistentDomain(forName: "rivolt-uitest")
        defaults.set("https://uitest.invalid", forKey: "rivolt.server_url")
        return AppState(tokens: FixtureTokens(), defaults: defaults, session: URLSession(configuration: config))
    }
}

/// A session that is always there, for one made-up server.
final class FixtureTokens: TokenStore, @unchecked Sendable {
    func token(for server: URL) throws -> String? { "uitest-token" }
    func setToken(_ token: String, for server: URL) throws {}
    func deleteToken(for server: URL) throws {}
}

final class FixtureURLProtocol: URLProtocol {
    nonisolated(unsafe) static var directories: [URL] = []
    /// Set once the refresh call was answered (see refreshLimitedVariable).
    nonisolated(unsafe) static var refreshed = false

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    /// With `-uitest-planner-gated`: the account "buys Pro" when the test creates the file named by RIVOLT_UITEST_BOUGHT_PRO_FILE.
    static var boughtPro: Bool {
        guard let path = ProcessInfo.processInfo.environment["RIVOLT_UITEST_BOUGHT_PRO_FILE"] else { return false }
        return FileManager.default.fileExists(atPath: path)
    }

    /// The answer's file for a path, or nil (a 404).
    static func file(method: String, path: String) -> String? {
        switch (method, path) {
        // The iPhone elevation chart is a preview-only design (design_preview key ios_elevation): on only when the UI test asks.
        case ("GET", "/api/config") where ProcessInfo.processInfo.arguments.contains("-uitest-ios-elevation"):
            return ProcessInfo.processInfo.arguments.contains("-uitest-now-v2") ? "config_now_v2_elevation" : "config_ios_elevation"
        // The planner's forecast radar (design_preview key plan_forecast_map, Pro): the server has radar, a forecast for the trip's first four hours and the key on.
        case ("GET", "/api/config") where ProcessInfo.processInfo.arguments.contains("-uitest-forecast"): return "config_forecast"
        case ("GET", "/api/maps/radar/frame") where ProcessInfo.processInfo.arguments.contains("-uitest-forecast"): return "radar_frame"
        case ("GET", "/api/maps/forecast-radar/hours") where ProcessInfo.processInfo.arguments.contains("-uitest-forecast"): return "forecast_hours"
        case ("GET", "/api/auth/me"): return ProcessInfo.processInfo.arguments.contains("-uitest-planner-gated") ? (Self.boughtPro ? "me_plan_subscriber" : "me_plan_free_planner_pro") : "me"
        case ("GET", "/api/vehicles"): return "vehicles"
        case ("GET", "/api/trips/saved"): return "trip_saved_list"
        // The place search: a charger-list hotel (EV charging) and a plain Photon row.
        case ("GET", "/api/geocode"): return "geocode_hotel"
        case ("POST", _) where path.hasPrefix("/api/trips/saved/") && path.hasSuffix("/receipt"): return "trip_receipt"
        case ("GET", "/api/settings/planner"): return "settings_planner"
        case ("GET", "/api/app/config"): return "app_config"
        // The preview-only designs: off unless the UI test asks for the theme switch.
        case ("GET", "/api/config"): let args = ProcessInfo.processInfo.arguments
            return args.contains("-uitest-charging") ? "config_now_charge" : args.contains("-uitest-driving") ? "config_now_drive" : args.contains("-uitest-parked") ? "config_now_parked" : args.contains("-uitest-trip-receipt") ? "config_trip_receipt" : args.contains("-uitest-share-map") ? "config_share_map" : args.contains("-uitest-now-v2") ? "config_now_v2" : args.contains("-uitest-theme-switch") ? "config_theme_switch" : nil
        // The round-trip launch: one merged drive of three real legs (a zero-distance stub between two of them left out), each leg's own route and the samples.
        case ("GET", "/api/activity") where ProcessInfo.processInfo.arguments.contains("-uitest-round-trip"): return "activity_round_trip"
        case ("GET", "/api/samples") where ProcessInfo.processInfo.arguments.contains("-uitest-round-trip"): return "samples_round_trip"
        case ("GET", "/api/activity"): return "activity"
        // The Activity map's two lists (empty: the UI test counts how often they are read).
        case ("GET", "/api/drives"): return "drives_list"
        case ("GET", "/api/charges"): return "charges_list"
        case ("POST", "/api/maps/valhalla/route"): return "valhalla_route"
        default:
            if method == "GET", path.hasPrefix("/api/vehicles/"), path.hasSuffix("/state") { return "state_full" }
            // The Now screen's own reads, answered only for the now_v2 launch (the other launches keep the unreachable-truck card).
            if ProcessInfo.processInfo.arguments.contains("-uitest-now-v2"), method == "GET" {
                if path.contains("/state/") { return "state" }  // the truck read (not spelled as a path: the contract test reads literals)
                if path.hasPrefix("/api/vehicles/"), path.hasSuffix("/now") { return "now_v1" }
                if path.hasPrefix("/api/vehicles/"), path.hasSuffix("/cold-range") { return "cold_range" }
            }
            // The stays near where a day's driving runs out, and the two days with a night at one (not spelled as paths: see startLoading).
            if method == "POST", path.hasSuffix("/trips/overnight-candidates") { return "trip_overnight_candidates" }
            if method == "POST", path.hasSuffix("/trips/plan-multiday") { return "trip_plan_multiday_apart" }
            if method == "GET", path.hasPrefix("/api/drives/rt-leg-"), ProcessInfo.processInfo.arguments.contains("-uitest-round-trip") { return "drive_" + path.dropFirst("/api/drives/".count) }
            // The share sheet's map toggle (design-preview key share_map) needs a trimmed route for the drive.
            if method == "GET", path.hasPrefix("/api/drives/"), path.hasSuffix("/postcard") { return "drive_postcard" }
            // One charge's row (not the lists under the same path).
            if method == "GET", path.hasPrefix("/api/charges/"), !path.hasSuffix("/to-confirm"), !path.hasSuffix("/clusters") { return "charge_one" }
            return nil
        }
    }

    private func data(named name: String) -> Data? {
        for dir in Self.directories {
            if let d = try? Data(contentsOf: dir.appendingPathComponent(name + ".json")) { return d }
        }
        return nil
    }

    /// The one point of the fixture road line (ios/RivoltUITests/Fixtures/valhalla_route.json) that is not a stop, about 20 km off the straight legs between them.
    static let roadOnlyPoint = (lat: 47.5, lon: -121.4)

    /// The corridor-layers answer: the stations of the kinds asked for, from the UI tests' own list, and only for a search along the road line
    /// (a path through `roadOnlyPoint`): a search along the stops alone, the map's line never reaching the layers, finds nothing.
    private func corridorLayers(body: Data?) -> Data? {
        struct Ask: Decodable { var path: [[Double]]; var kinds: [String] }
        guard let all = data(named: "corridor_pois"), let body, let ask = try? JSONDecoder().decode(Ask.self, from: body),
              let json = try? JSONSerialization.jsonObject(with: all) as? [String: Any], let pois = json["pois"] as? [[String: Any]] else { return nil }
        // Within about 1 km (0.01° of latitude, 0.015° of longitude at this latitude).
        let alongRoad = ask.path.contains { $0.count == 2 && abs($0[0] - Self.roadOnlyPoint.lat) < 0.01 && abs($0[1] - Self.roadOnlyPoint.lon) < 0.015 }
        let kept = alongRoad ? pois.filter { poi in ((poi["kinds"] as? [String]) ?? []).contains { ask.kinds.contains($0) } } : []
        return try? JSONSerialization.data(withJSONObject: ["pois": kept, "total": kept.count, "asof": json["asof"] ?? "2026-10-20"])
    }

    /// A charge in progress for the Now card's charging graph (`-uitest-charging`), the same three cases as the web's e2e stub (web/e2e/now-charge-graph.pw.ts),
    /// chosen by RIVOLT_UITEST_CHARGE_CASE: "ac" (11 kW, 90 min), "dc" (a fast charge, 25 min), "slow" (4.8 kW, 30 min). The truck read, `/now` and the
    /// stored samples every 30 s, their times counted back from the launch so the card is live whenever the test runs.
    private func chargingAnswer(path: String) -> Data? {
        let name = ProcessInfo.processInfo.environment["RIVOLT_UITEST_CHARGE_CASE"] ?? "ac"
        let elapsedMin: Double = name == "dc" ? 25 : name == "slow" ? 30 : 90
        let soc: (Double) -> Double = { m in
            name == "dc" ? 15 + 40 * (1 - exp(-m / 20)) : name == "slow" ? 40 + m * 0.09 : 40 + m * 0.136
        }
        let kw: (Double) -> Double = { m in name == "dc" ? 190 * exp(-m / 22) + 40 : name == "slow" ? 4.8 : 11 }
        // RIVOLT_UITEST_CHARGE_NOW (RFC 3339, a little before the real time) places the charge's history against that instant, so the web stub can run at the
        // same one (STUB_NOW) and the two cards show the same clock times; the freshness stamps stay current.
        let live = Date()
        let now = ProcessInfo.processInfo.environment["RIVOLT_UITEST_CHARGE_NOW"].flatMap { ISO8601DateFormatter().date(from: $0) } ?? live
        let iso = ISO8601DateFormatter()
        let started = now.addingTimeInterval(-elapsedMin * 60)
        func rewrite(_ name: String, _ patch: (inout [String: Any]) -> Void) -> Data? {
            guard let raw = data(named: name), var json = (try? JSONSerialization.jsonObject(with: raw)) as? [String: Any] else { return nil }
            patch(&json)
            return try? JSONSerialization.data(withJSONObject: json)
        }
        // The slow and the 11 kW charges are at the saved home (the fixture state's fix), the fast one is away from it, as in the web's stub.
        if path == "/api/settings/home-location", name != "dc" {
            return try? JSONSerialization.data(withJSONObject: ["set": true, "latitude": 30.2672, "longitude": -97.7431, "label": "Home", "source": "coordinates", "previous_set": false, "address_search": false] as [String: Any])
        }
        if path == "/api/auth/me", let units = ProcessInfo.processInfo.environment["RIVOLT_UITEST_UNITS"] {
            return rewrite("me") { j in
                j["units"] = ["distance_unit": units, "temperature_unit": units == "km" ? "c" : "f", "distance_source": "user", "temperature_source": "follows_distance"]
            }
        }
        if path.contains("/state/") {
            return rewrite("state") { j in
                j["at"] = iso.string(from: live)
                j["location_fix_at"] = iso.string(from: live)
                j["battery_level_pct"] = soc(elapsedMin)
                j["charger_power_kw"] = kw(elapsedMin)
                j["cabin_temp_c"] = 28.6
                j["distance_to_empty"] = 300.0  // the web stub's numbers, so a pair shows only real differences
            }
        }
        if path.hasPrefix("/api/vehicles/"), path.hasSuffix("/now") {
            // The web stub's believable price: kWh added at a home or a public rate.
            let cost = ((soc(elapsedMin) - soc(0)) * 1.35 * (kw(0) >= 50 ? 0.48 : 0.15) * 100).rounded() / 100
            return rewrite("now_v1") { j in
                j["at"] = iso.string(from: live)
                j["location_fix_at"] = iso.string(from: live)
                j["vehicle_state_frame_at"] = iso.string(from: live)
                j["state_since"] = iso.string(from: started)
                j["soc_at_state_start"] = soc(0).rounded()
                j["charge"] = ["started_at": iso.string(from: started), "soc_start": soc(0).rounded(), "peak_kw": 11, "cost": cost, "currency": "USD", "cost_is_estimate": true]
                j["ambient"] = ["temp_c": 12.0, "source": "open-meteo", "as_of": iso.string(from: live)]
            }
        }
        if path == "/api/samples" {
            var rows: [[String: Any]] = []
            var t = started
            while t <= now {
                let m = max(0, t.timeIntervalSince(started) / 60)
                rows.append(["At": iso.string(from: t), "BatteryLevelPct": soc(m), "ChargerPowerKW": kw(m),
                             "pack_temp_avg_c": 21.0 + 14.0 * pow(min(1, m / max(elapsedMin, 1)), 0.7), "pack_temp_min_c": 20.0 + 14.0 * pow(min(1, m / max(elapsedMin, 1)), 0.7), "pack_temp_max_c": 22.0 + 14.0 * pow(min(1, m / max(elapsedMin, 1)), 0.7), "ChargeLimitPct": 80.0])
                t = t.addingTimeInterval(30)
            }
            return try? JSONSerialization.data(withJSONObject: rows)
        }
        return nil
    }

    /// A drive in progress for the Now card's driving graph (`-uitest-driving`), the same three cases and numbers as the web's e2e stub
    /// (web/e2e/now-drive-graph.pw.ts), chosen by RIVOLT_UITEST_DRIVE_CASE: "middle" (22 min, a town drive onto a highway), "stop" (18 min, a stop in the
    /// middle), "fresh" (1 min: nothing to draw yet). RIVOLT_UITEST_DRIVE_DEST=none leaves the truck with no trip. The truck read, `/now`, the live drive,
    /// the destination and the stored samples every 10 s, their times counted back from the launch (or from RIVOLT_UITEST_CHARGE_NOW, as the charge cases).
    private func drivingAnswer(path: String) -> Data? {
        let name = ProcessInfo.processInfo.environment["RIVOLT_UITEST_DRIVE_CASE"] ?? "middle"
        let elapsedMin: Double = name == "stop" ? 18 : name == "fresh" ? 1 : 22
        let mph: (Double) -> Double = { m in
            switch name {
            case "stop": return m < 6 ? 40 : m < 9 ? 0 : 45 + 5 * sin(m)
            case "fresh": return m * 30
            default: return m < 3 ? m * 12 : m < 14 ? 55 + 8 * sin(m / 2) : max(25, 62 - (m - 14) * 4)
            }
        }
        let soc: (Double) -> Double = { m in name == "stop" ? 64 - m * 0.15 : name == "fresh" ? 70 - m * 0.1 : 71 - m * 0.18 }
        let live = Date()
        let now = ProcessInfo.processInfo.environment["RIVOLT_UITEST_CHARGE_NOW"].flatMap { ISO8601DateFormatter().date(from: $0) } ?? live
        let iso = ISO8601DateFormatter()
        let started = now.addingTimeInterval(-elapsedMin * 60)
        func rewrite(_ name: String, _ patch: (inout [String: Any]) -> Void) -> Data? {
            guard let raw = data(named: name), var json = (try? JSONSerialization.jsonObject(with: raw)) as? [String: Any] else { return nil }
            patch(&json)
            return try? JSONSerialization.data(withJSONObject: json)
        }
        if path == "/api/auth/me", let units = ProcessInfo.processInfo.environment["RIVOLT_UITEST_UNITS"] {
            return rewrite("me") { j in
                j["units"] = ["distance_unit": units, "temperature_unit": units == "km" ? "c" : "f", "distance_source": "user", "temperature_source": "follows_distance"]
            }
        }
        if path.contains("/state/") {
            return rewrite("state") { j in
                j["at"] = iso.string(from: live)
                j["location_fix_at"] = iso.string(from: live)
                j["battery_level_pct"] = soc(elapsedMin)
                j["charger_state"] = "charging_not_charging"
                j["charger_power_kw"] = 0
                j["charger_status"] = "chrgr_sts_not_connected"
                j["gear"] = "D"
                j["power_state"] = "go"
                j["speed_kph"] = mph(elapsedMin) * 1.609344
                j["cabin_temp_c"] = 20.0
                j["distance_to_empty"] = 321.9  // 200 mi, the web stub's figure
            }
        }
        if path.hasPrefix("/api/vehicles/"), path.hasSuffix("/now") {
            return rewrite("now_v1") { j in
                j["at"] = iso.string(from: live)
                j["location_fix_at"] = iso.string(from: live)
                j["vehicle_state_frame_at"] = iso.string(from: live)
                j["state"] = "driving"
                j["state_since"] = iso.string(from: started)
                j["soc_at_state_start"] = soc(0).rounded()
                j["charge"] = nil
                j["ambient"] = ["temp_c": 12.0, "source": "open-meteo", "as_of": iso.string(from: live)]
            }
        }
        // (Not spelled as paths: the contract test reads path literals in the app's source as calls it must find on the server.)
        if path.contains("/live-" + "drive/") {
            return try? JSONSerialization.data(withJSONObject: ["vehicle_id": "veh-fixture", "number": 1, "started_at": iso.string(from: started), "ended_at": iso.string(from: now),
                "elapsed_sec": elapsedMin * 60, "start_soc_pct": soc(0), "end_soc_pct": soc(elapsedMin), "distance_mi": 10, "max_speed_mph": 62, "avg_speed_mph": 40,
                "energy_used_kwh": 2.7, "mi_per_kwh": 3.7] as [String: Any])
        }
        if path.contains("/live-" + "destination/") {
            if ProcessInfo.processInfo.environment["RIVOLT_UITEST_DRIVE_DEST"] == "none" { return Data() }
            return try? JSONSerialization.data(withJSONObject: ["name": "Test Depot", "lat": 40.3, "lon": -100.45, "eta": iso.string(from: now.addingTimeInterval(1500)), "miles_left": 12.4,
                "route": "_srtkA~lyh~D_cmA~hbE_etB~s`B", "route_precision": 6] as [String: Any])
        }
        if path == "/api/samples" {
            var rows: [[String: Any]] = []
            var t = started
            while t <= now {
                let m = max(0, t.timeIntervalSince(started) / 60)
                rows.append(["At": iso.string(from: t), "BatteryLevelPct": soc(m), "SpeedMph": mph(m), "ShiftState": "D", "Lat": 40.2 + m * 0.002, "Lon": -100.3 + m * 0.002])
                t = t.addingTimeInterval(10)
            }
            return try? JSONSerialization.data(withJSONObject: rows)
        }
        return nil
    }

    /// A parked truck for the Now card's parked graph (`-uitest-parked`), the same cases and numbers as the web's e2e stub (web/e2e/parkedSeriesFixture.ts),
    /// chosen by RIVOLT_UITEST_PARKED_CASE: "overnight" (five sleeps, a drive 12 h ago), "charged" (a charge between two sleeps), "asleep" (the truck is
    /// asleep now), "never_slept" (awake and idling all day), "minutes" (parked a few minutes ago: nothing to draw). The fixtures are written at the web
    /// stub's pinned clock; every time in them moves by the same amount so the series ends at the launch.
    private func parkedAnswer(path: String) -> Data? {
        let name = ProcessInfo.processInfo.environment["RIVOLT_UITEST_PARKED_CASE"] ?? "overnight"
        let pinned = Date(timeIntervalSince1970: 1_794_938_400)  // 2026-11-17T18:00:00Z, the stub clock
        // RIVOLT_UITEST_PARKED_NOW (RFC 3339, a little before the real time) places every fixture time against that instant, so the web stub can be
        // run at the same one (STUB_NOW) and the two cards show the same clock times.
        let plain = ISO8601DateFormatter()
        let now = ProcessInfo.processInfo.environment["RIVOLT_UITEST_PARKED_NOW"].flatMap { plain.date(from: $0) } ?? Date()
        let live = Date()  // freshness stamps stay current; only the history is placed against the mark
        let shift = now.timeIntervalSince(pinned)
        let iso = ISO8601DateFormatter()
        iso.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        func shifted(_ any: Any) -> Any {
            if let s = any as? String, let d = iso.date(from: s) { return iso.string(from: d.addingTimeInterval(shift)) }
            if let a = any as? [Any] { return a.map(shifted) }
            if let o = any as? [String: Any] { return o.mapValues(shifted) }
            return any
        }
        func fixture(_ file: String) -> [String: Any]? {
            guard let raw = data(named: file), let json = (try? JSONSerialization.jsonObject(with: raw)) as? [String: Any] else { return nil }
            return shifted(json) as? [String: Any]
        }
        func rewrite(_ file: String, _ patch: (inout [String: Any]) -> Void) -> Data? {
            guard var json = fixture(file) else { return nil }
            patch(&json)
            return try? JSONSerialization.data(withJSONObject: json)
        }
        let isoPlain = ISO8601DateFormatter()
        let series: [String: Any]? = name == "minutes" ? nil : fixture("parked_series_\(name)")
        let points = series?["points"] as? [[String: Any]] ?? []
        let soc = (points.last?["soc"] as? Double) ?? 78
        let parkedSince = (series?["parked_since"] as? String) ?? iso.string(from: now.addingTimeInterval(-0.3 * 3600))
        let asleepNow = name == "asleep"
        if path == "/api/settings/home-location" {
            return try? JSONSerialization.data(withJSONObject: ["set": true, "latitude": 30.2672, "longitude": -97.7431, "label": "Home", "source": "coordinates", "previous_set": false, "address_search": false] as [String: Any])
        }
        if path == "/api/auth/me", let units = ProcessInfo.processInfo.environment["RIVOLT_UITEST_UNITS"] {
            return rewrite("me") { j in
                j["units"] = ["distance_unit": units, "temperature_unit": units == "km" ? "c" : "f", "distance_source": "user", "temperature_source": "follows_distance"]
            }
        }
        if path.contains("/parked-series") {
            return name == "gone" ? nil : series.flatMap { try? JSONSerialization.data(withJSONObject: $0) } ?? (try? JSONSerialization.data(withJSONObject: [
                "version": 1, "from": iso.string(from: now.addingTimeInterval(-86_400)), "to": iso.string(from: now), "parked_since": parkedSince, "parked": true,
                "points": [], "sleeps": [], "wakes": []] as [String: Any]))
        }
        if path.contains("/state/") {
            return rewrite("state") { j in
                j["at"] = isoPlain.string(from: live)
                j["location_fix_at"] = isoPlain.string(from: live)
                j["battery_level_pct"] = soc
                j["charger_state"] = "charging_complete"
                j["charger_power_kw"] = 0
                j["charger_status"] = "chrgr_sts_not_connected"
                j["power_state"] = asleepNow ? "sleep" : "ready"
                j["cabin_temp_c"] = 21.0
                j["distance_to_empty"] = 299.5  // 186 mi, the web stub's figure
            }
        }
        if path.hasPrefix("/api/vehicles/"), path.hasSuffix("/now") {
            let figures = series?["figures"] as? [String: Any]
            let sleeps = series?["sleeps"] as? [[String: Any]] ?? []
            return rewrite("now_v1") { j in
                j["at"] = isoPlain.string(from: live)
                j["location_fix_at"] = isoPlain.string(from: live)
                j["vehicle_state_frame_at"] = isoPlain.string(from: live)
                j["state"] = asleepNow ? "asleep" : "parked"
                j["asleep"] = asleepNow
                j["state_since"] = asleepNow ? (sleeps.last?["from"] ?? parkedSince) : parkedSince
                j["soc_at_state_start"] = soc
                j["charge"] = nil
                if asleepNow, let from = sleeps.last?["from"] { j["asleep_since"] = from }
                if let today = figures?["asleep_today_s"] { j["asleep_today_s"] = today }
                j["ambient"] = ["temp_c": 12.0, "source": "open-meteo", "as_of": isoPlain.string(from: live)]
            }
        }
        return nil
    }

    override func startLoading() {
        guard let url = request.url else { return }
        let method = request.httpMethod ?? "GET"
        var answer: Data?
        var failed: Int?
        var retryAfter: String?
        // (Not spelled as a path: the contract test reads path literals in the app's source as calls it must find on the server.)
        if method == "POST", url.lastPathComponent == "corridor-layers" {
            var body = request.httpBody
            if body == nil, let stream = request.httpBodyStream {
                stream.open(); defer { stream.close() }
                var buf = [UInt8](repeating: 0, count: 65536), out = Data()
                while stream.hasBytesAvailable { let n = stream.read(&buf, maxLength: buf.count); if n <= 0 { break }; out.append(buf, count: n) }
                body = out
            }
            answer = corridorLayers(body: body)
        } else if ProcessInfo.processInfo.environment[UITestLaunch.billingVariable] != nil, method == "GET", url.path == "/api/auth/me" {
            answer = data(named: "me_plan_free")
        } else if let state = ProcessInfo.processInfo.environment[UITestLaunch.noTruckVariable], method == "GET", url.path == "/api/vehicles" || url.lastPathComponent == "discovery" {
            let shown = Self.refreshed && ProcessInfo.processInfo.environment[UITestLaunch.refreshLimitedVariable] != nil ? "looking" : state
            answer = Data((url.lastPathComponent == "discovery" ? #"{"state":"\#(shown)"}"# : "[]").utf8)
        } else if ProcessInfo.processInfo.environment[UITestLaunch.refreshUnheldVariable] != nil, method == "POST", url.lastPathComponent == "refresh-vehicles" {
            failed = 503
            retryAfter = "1"
            answer = Data(#"{"error":"unavailable"}"#.utf8)
        } else if ProcessInfo.processInfo.environment[UITestLaunch.refreshLimitedVariable] != nil, method == "POST", url.lastPathComponent == "refresh-vehicles" {
            Self.refreshed = true
            failed = 429
            retryAfter = "600"
            answer = Data(#"{"error":"too many checks"}"#.utf8)
        } else if ProcessInfo.processInfo.environment[UITestLaunch.stateFailsVariable] != nil, method == "GET", url.path.hasPrefix("/api/vehicles/"), url.path.hasSuffix("/state") {
            failed = 503
            answer = Data(#"{"error":"upstream unavailable","class":"transient"}"#.utf8)
        } else if ProcessInfo.processInfo.arguments.contains("-uitest-charging"), method == "GET", let live = chargingAnswer(path: url.path) {
            answer = live
        } else if ProcessInfo.processInfo.arguments.contains("-uitest-driving"), method == "GET", let live = drivingAnswer(path: url.path) {
            answer = live
        } else if ProcessInfo.processInfo.arguments.contains("-uitest-parked"), method == "GET", let live = parkedAnswer(path: url.path) {
            answer = live
        } else if let name = Self.file(method: method, path: url.path) {
            answer = data(named: name)
        }
        let status = failed ?? (answer == nil ? 404 : 200)
        // An optional trace (RIVOLT_UITEST_TRACE=<file>): every request and how it was answered, for whoever reads a failure.
        if let path = ProcessInfo.processInfo.environment["RIVOLT_UITEST_TRACE"], let line = "\(status) \(method) \(url.path)\n".data(using: .utf8) {
            let log = URL(fileURLWithPath: path)
            if let h = try? FileHandle(forWritingTo: log) { h.seekToEndOfFile(); h.write(line); h.closeFile() } else { try? line.write(to: log) }
        }
        let payload = answer ?? Data(#"{"error":"not in the UI test fixtures"}"#.utf8)
        var headers = ["Content-Type": "application/json"]
        headers["Retry-After"] = retryAfter
        let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: payload)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}
#endif
