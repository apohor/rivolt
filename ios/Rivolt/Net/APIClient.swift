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

    init(baseURL: URL, tokens: TokenStore) {
        self.baseURL = baseURL
        self.tokens = tokens
        let config = URLSessionConfiguration.ephemeral
        // No cookies: the app authenticates with its bearer token only.
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        // Timeouts matter on cellular: the default (60s) leaves a
        // frozen-looking UI for a full minute when a self-hosted server is
        // unreachable. 15s is plenty for the JSON surface.
        config.timeoutIntervalForRequest = 15
        config.timeoutIntervalForResource = 30
        self.session = URLSession(configuration: config)

        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        self.decoder = decoder

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

    func logout() async throws {
        let _: Empty = try await request("POST", "/api/auth/logout")
    }

    func me() async throws -> AuthUser {
        try await request("GET", "/api/auth/me")
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

    // MARK: - Core

    private struct Empty: Decodable {}

    private func request<Response: Decodable>(
        _ method: String,
        _ path: String,
        body: (any Encodable)? = nil,
        authenticated: Bool = true
    ) async throws -> Response {
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
            // Surface the server's error body verbatim when possible.
            // Rivolt returns either JSON `{"error": "..."}` or plain
            // text from http.Error; try the former first.
            let message: String
            if let j = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let e = j["error"] as? String, !e.isEmpty {
                message = e
            } else {
                message = String(data: data, encoding: .utf8) ?? "HTTP \(http.statusCode)"
            }
            throw APIError.server(status: http.statusCode, message: message)
        }
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
}

enum APIError: LocalizedError {
    case badURL
    case transport(String)
    case server(status: Int, message: String)
    case decoding(Error)

    var errorDescription: String? {
        switch self {
        case .badURL: return "Invalid server URL."
        case .transport(let m): return m
        case .server(_, let m): return m
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
