#if DEBUG
import Foundation

/// A launch for the UI tests (RivoltUITests): the app signed in, talking to no server. The test sets RIVOLT_UITEST_FIXTURES to the directories of JSON
/// answers, colon-separated, the first that has a file winning (the UI tests' own, then the unit tests' fixtures), and every request the app makes is answered from them in the app's own URLSession: an
/// unknown path is a 404, so nothing reaches a real server. Debug builds only: Release has none of this.
enum UITestLaunch {
    static let variable = "RIVOLT_UITEST_FIXTURES"
    /// An account with no vehicle yet: the vehicles list is empty and the discovery answer is this state (looking, none, reauth), for the connect screen.
    static let noTruckVariable = "RIVOLT_UITEST_NO_TRUCK"

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
        case ("GET", "/api/activity"): return "activity"
        default:
            if method == "GET", path.hasPrefix("/api/vehicles/"), path.hasSuffix("/state") { return "state_full" }
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

    /// The corridor-layers answer: the stations of the kinds asked for, from the UI tests' own list.
    private func corridorLayers(body: Data?) -> Data? {
        struct Ask: Decodable { var kinds: [String] }
        guard let all = data(named: "corridor_pois"), let body, let ask = try? JSONDecoder().decode(Ask.self, from: body),
              let json = try? JSONSerialization.jsonObject(with: all) as? [String: Any], let pois = json["pois"] as? [[String: Any]] else { return nil }
        let kept = pois.filter { poi in ((poi["kinds"] as? [String]) ?? []).contains { ask.kinds.contains($0) } }
        return try? JSONSerialization.data(withJSONObject: ["pois": kept, "total": kept.count, "asof": json["asof"] ?? "2026-10-20"])
    }

    override func startLoading() {
        guard let url = request.url else { return }
        let method = request.httpMethod ?? "GET"
        var answer: Data?
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
            answer = Data((url.lastPathComponent == "discovery" ? #"{"state":"\#(state)"}"# : "[]").utf8)
        } else if let name = Self.file(method: method, path: url.path) {
            answer = data(named: name)
        }
        let status = answer == nil ? 404 : 200
        // An optional trace (RIVOLT_UITEST_TRACE=<file>): every request and how it was answered, for whoever reads a failure.
        if let path = ProcessInfo.processInfo.environment["RIVOLT_UITEST_TRACE"], let line = "\(status) \(method) \(url.path)\n".data(using: .utf8) {
            let log = URL(fileURLWithPath: path)
            if let h = try? FileHandle(forWritingTo: log) { h.seekToEndOfFile(); h.write(line); h.closeFile() } else { try? line.write(to: log) }
        }
        let payload = answer ?? Data(#"{"error":"not in the UI test fixtures"}"#.utf8)
        let response = HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: payload)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}
#endif
