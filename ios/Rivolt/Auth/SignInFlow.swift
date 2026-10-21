import Foundation

/// The browser step of sign-in: opens the server's start page and returns the
/// URL the server finally redirects to. `SystemWebAuthenticator` is the real
/// one (ASWebAuthenticationSession); tests use a fake.
@MainActor
protocol WebAuthenticator {
    func authenticate(start: URL, callback: SignInCallback) async throws -> URL
}

/// Where the sign-in comes back to the app. Chosen here from the server's
/// address, compiled in; the server alone decides where it redirects, so this
/// only picks what the app listens for.
enum SignInCallback: Equatable {
    /// rivolt.dev's Universal Link (associated domain, AASA from the server).
    case https(host: String, path: String)
    /// Self-hosted servers (and preview): `RIVOLT_APP_CALLBACK_URL=rivolt://auth`.
    case customScheme(String)

    static let universalLinkHost = "rivolt.dev"

    /// rivolt.dev, on any port, only ever uses its https callback, never the
    /// scheme, which any app on the phone can claim (DA). Every other server
    /// uses rivolt://auth.
    static func forServer(_ server: URL) -> SignInCallback {
        var host = (server.host ?? "").lowercased()
        while host.hasSuffix(".") { host.removeLast() }
        if host == universalLinkHost {
            return .https(host: universalLinkHost, path: "/app/callback")
        }
        return .customScheme("rivolt")
    }

    /// Exact match: scheme, host, path, no port or credentials.
    func matches(_ url: URL) -> Bool {
        guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false),
              c.port == nil, c.user == nil, c.password == nil, c.fragment == nil else {
            return false
        }
        switch self {
        case let .https(host, path):
            return c.scheme == "https" && c.host == host && c.path == path
        case let .customScheme(scheme):
            return c.scheme == scheme && c.host == "auth" && c.path.isEmpty
        }
    }

    enum Outcome: Equatable {
        case code(String)
        case denied
        case failed
    }

    /// What the server said: a one-time code, or that the user cancelled on
    /// the consent page. Anything else, including a callback that isn't
    /// exactly this one or a code with unexpected characters, is a failure.
    func outcome(of url: URL) -> Outcome {
        guard matches(url),
              let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems else {
            return .failed
        }
        let codes = items.filter { $0.name == "code" }
        let errors = items.filter { $0.name == "error" }
        if codes.isEmpty, errors.count == 1 {
            return errors[0].value == "access_denied" ? .denied : .failed
        }
        guard errors.isEmpty, codes.count == 1, let code = codes[0].value, Self.isCode(code) else {
            return .failed
        }
        return .code(code)
    }

    /// The server's codes are 32 random bytes, base64url without padding.
    static func isCode(_ s: String) -> Bool {
        (20...128).contains(s.count) && s.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-" || $0 == "_") }
    }
}

/// One sign-in against one server (design §2): PKCE, the browser, the
/// one-time code, the token. Nothing here is logged, and the verifier lives
/// only in `run`'s locals, so it is used once and dropped whatever happens.
@MainActor
struct SignInFlow {
    /// Captured when sign-in starts: the token is stored under this origin
    /// whatever the app's server setting says when the browser returns.
    let server: URL
    /// Talks to `server` only.
    let client: APIClient
    let tokens: TokenStore
    let authenticator: WebAuthenticator
    /// UIDevice.current.name: "iPhone" on iOS 16 and later. Sent as is.
    let deviceName: String

    static func startURL(server: URL, challenge: String) -> URL? {
        guard var c = URLComponents(url: server, resolvingAgainstBaseURL: false) else { return nil }
        c.path = "/api/auth/app/start"
        c.queryItems = [
            URLQueryItem(name: "code_challenge", value: challenge),
            URLQueryItem(name: "code_challenge_method", value: "S256"),
        ]
        return c.url
    }

    /// Signs in and stores the token. Returns the username.
    func run() async throws -> String {
        let pkce = try PKCE.generate()
        guard let start = Self.startURL(server: server, challenge: pkce.challenge) else {
            throw SignInError.failed
        }
        let callback = SignInCallback.forServer(server)
        let returned = try await authenticator.authenticate(start: start, callback: callback)
        let code: String
        switch callback.outcome(of: returned) {
        case .denied: throw SignInError.denied
        case .failed: throw SignInError.failed
        case .code(let c): code = c
        }
        let token: AppToken
        do {
            token = try await client.appToken(code: code, verifier: pkce.verifier, deviceName: deviceName)
        } catch APIError.server(400, _) {
            // invalid_grant: expired, already used, or not this app's verifier.
            throw SignInError.expired
        }
        try tokens.setToken(token.token, for: server)
        return token.user.username
    }
}
