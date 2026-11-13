#if DEBUG
import Foundation

/// A launch for the UI tests (RivoltUITests): the app signed in, talking to no server. The test sets RIVOLT_UITEST_FIXTURES to the directories of JSON
/// answers, colon-separated, the first that has a file winning (the UI tests' own, then the unit tests' fixtures), and every request the app makes is answered from them in the app's own URLSession: an
/// unknown path is a 404, so nothing reaches a real server. Debug builds only: Release has none of this.
enum UITestLaunch {
    static let variable = "RIVOLT_UITEST_FIXTURES"
    /// An account with no vehicle yet: the vehicles list is empty and the discovery answer is this state (looking, none, reauth), for the connect screen.
    static let noTruckVariable = "RIVOLT_UITEST_NO_TRUCK"
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

    /// The answer's file for a path, or nil (a 404).
    static func file(method: String, path: String) -> String? {
        switch (method, path) {
        case ("GET", "/api/auth/me"): return "me"
        case ("GET", "/api/vehicles"): return "vehicles"
        case ("GET", "/api/trips/saved"): return "trip_saved_list"
        case ("GET", "/api/settings/planner"): return "settings_planner"
        case ("GET", "/api/app/config"): return "app_config"
        // The preview-only designs: off unless the UI test asks for the theme switch.
        case ("GET", "/api/config"): let args = ProcessInfo.processInfo.arguments
            return args.contains("-uitest-now-v2") ? "config_now_v2" : args.contains("-uitest-theme-switch") ? "config_theme_switch" : nil
        case ("GET", "/api/activity"): return "activity"
        case ("POST", "/api/maps/valhalla/route"): return "valhalla_route"
        default:
            if method == "GET", path.hasPrefix("/api/vehicles/"), path.hasSuffix("/state") { return "state_full" }
            // The Now screen's own reads, answered only for the now_v2 launch (the other launches keep the unreachable-truck card).
            if ProcessInfo.processInfo.arguments.contains("-uitest-now-v2"), method == "GET" {
                if path.hasPrefix("/api/state/") { return "state" }
                if path.hasPrefix("/api/vehicles/"), path.hasSuffix("/now") { return "now_v1" }
            }
            // The stays near where a day's driving runs out, and the two days with a night at one (not spelled as paths: see startLoading).
            if method == "POST", path.hasSuffix("/trips/overnight-candidates") { return "trip_overnight_candidates" }
            if method == "POST", path.hasSuffix("/trips/plan-multiday") { return "trip_plan_multiday_apart" }
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
