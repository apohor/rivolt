import Foundation

/// The widget's reads: the same two GETs the app's Home makes, on Rivolt's own
/// server only, with the token the app stored for that server. Read-only: no
/// request sends anything to the truck, and nothing here talks to Rivian.
///
/// Nothing is logged, and a redirect is never followed (so the token cannot be
/// sent to another host).
enum WidgetFetch {
    enum Outcome: Equatable {
        case snapshot(WidgetSnapshot)
        /// No token for this server.
        case signedOut
        /// The server said 401, 403 or 404: the session or the truck is gone.
        case revoked
        /// Anything else (offline, a server error, an unreadable answer).
        case failed
    }

    /// Only the fields the widget uses. A location in the answer is not decoded.
    private struct StateWire: Decodable {
        let batteryLevelPct: Double
        let distanceToEmpty: Double
        let gear: String?
        let powerState: String?
        let chargerState: String?
        let chargerPowerKw: Double?
        let chargeTargetPct: Double?
        let at: Date?
    }

    private struct VehicleID: Decodable {
        let id: String
    }

    /// A vehicle id goes into a URL path, so only ASCII letters, digits, "-" and "_".
    static func validID(_ s: String) -> Bool {
        !s.isEmpty && s.count <= 64 && s.unicodeScalars.allSatisfy {
            ($0.value < 128) && (CharacterSet.alphanumerics.contains($0) || $0 == "-" || $0 == "_")
        }
    }

    /// A GET to `path` (always under /api/) on `server`, with the bearer token. Nil for anything that
    /// would leave the token's own origin.
    static func request(_ path: String, server: URL, token: String) -> URLRequest? {
        guard path.hasPrefix("/api/"), !path.contains("//"), !path.contains("?"), !path.contains("#"),
              let url = URL(string: path, relativeTo: server)?.absoluteURL,
              url.scheme == "https", url.host?.lowercased() == server.host?.lowercased(), url.port == server.port else { return nil }
        var req = URLRequest(url: url)
        req.httpMethod = "GET"
        req.timeoutInterval = 15
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        return req
    }

    private final class NoRedirect: NSObject, URLSessionTaskDelegate {
        func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                        newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
            completionHandler(nil)
        }
    }

    private enum Reply {
        case ok(Data)
        case gone(Int)
        case failed
    }

    private static func get(_ req: URLRequest, session: URLSession) async -> Reply {
        guard let (data, resp) = try? await session.data(for: req, delegate: NoRedirect()),
              let http = resp as? HTTPURLResponse else { return .failed }
        switch http.statusCode {
        case 200: return .ok(data)
        case 401, 403, 404: return .gone(http.statusCode)
        default: return .failed
        }
    }

    static func load(store: WidgetStore, tokens: TokenStore = KeychainTokenStore(), session: URLSession = WidgetFetch.makeSession(),
                     clock: Date = .now) async -> Outcome {
        let server = store.server ?? ServerURL.default
        guard let token = (try? tokens.token(for: server)) ?? nil else { return .signedOut }

        var vehicleID = store.vehicleID
        if vehicleID == nil {
            guard let req = request("/api/vehicles", server: server, token: token) else { return .failed }
            switch await get(req, session: session) {
            case .ok(let data):
                guard let first = (try? JSON.decoder().decode([VehicleID].self, from: data))?.first, validID(first.id) else { return .failed }
                vehicleID = first.id
            case .gone: return .revoked
            case .failed: return .failed
            }
        }
        guard let id = vehicleID,
              let stateReq = request("/api/state/\(id)", server: server, token: token),
              let nowReq = request("/api/vehicles/\(id)/now", server: server, token: token) else { return .failed }

        async let stateReply = get(stateReq, session: session)
        async let nowReply = get(nowReq, session: session)
        let (st, nw) = await (stateReply, nowReply)

        var input: NowSummary.Input?
        switch st {
        case .gone: return .revoked
        case .failed: return .failed
        case .ok(let data):
            guard let wire = try? JSON.decoder().decode(StateWire.self, from: data) else { return .failed }
            input = NowSummary.Input(socPct: wire.batteryLevelPct, rangeKm: wire.distanceToEmpty, chargeTargetPct: wire.chargeTargetPct,
                                     gear: wire.gear, powerState: wire.powerState, chargerState: wire.chargerState,
                                     chargerPowerKw: wire.chargerPowerKw, at: wire.at)
        }
        var now: NowSnapshot?
        switch nw {
        // 401 and 403 mean the session is gone; a 404 on /now alone is a server that predates it, so /state is used.
        case .gone(let code) where code != 404: return .revoked
        case .ok(let data): now = try? JSON.decoder().decode(NowSnapshot.self, from: data)
        default: now = nil
        }
        guard let snapshot = WidgetSnapshot.make(input: input, now: now, clock: clock) else { return .failed }
        return .snapshot(snapshot)
    }

    static func makeSession() -> URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.timeoutIntervalForRequest = 15
        c.timeoutIntervalForResource = 25
        return URLSession(configuration: c)
    }
}
