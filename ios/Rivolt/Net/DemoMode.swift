import Foundation

/// The demo truck: the app against demo.rivolt.dev's anonymous, read-only sample account, so a reviewer or a new user can look around
/// without a Rivian. Everything here is on purpose small and fixed:
/// - the address is compiled in, never the server the person chose, so a demo request can only go to the demo;
/// - the demo client has its own ephemeral session (no cookies, no credentials, no cache on disk) and sends no Authorization header;
/// - it refuses anything but a read, and reads a 401 or 403 (a path the sample account is not served on) as "not here";
/// - nothing of it is stored: no token, no widget or App Group data, no notification registration.
enum DemoMode {
    static let baseURL = URL(string: "https://demo.rivolt.dev")!
    static let caption = "Sample truck · example data"
    static let about = "A sample truck with made-up data. Nothing here is saved or sent to Rivian. Sign in to see your own truck."
}

/// No token, ever: the demo reads nothing from the Keychain and can write nothing to it.
struct NoTokenStore: TokenStore {
    func token(for server: URL) throws -> String? { nil }
    func setToken(_ token: String, for server: URL) throws { throw APIError.transport("The demo truck keeps no sign-in.") }
    func deleteToken(for server: URL) throws {}
}

extension APIClient {
    /// The client for the demo truck. `session` is for tests (a stubbed URLProtocol).
    static func demo(session: URLSession? = nil) -> APIClient {
        APIClient(baseURL: DemoMode.baseURL, tokens: NoTokenStore(), session: session, demo: true)
    }
}
