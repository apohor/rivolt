import Foundation
import Observation

/// Global app state. Owns the `APIClient` (so every view talks to the same
/// server with the same token store) and the current authenticated user.
///
/// The three legal UI phases are represented by `Phase` rather than a pair of
/// optional booleans: that makes the root view a single `switch` and prevents
/// the "not-yet-loaded vs. logged-out" ambiguity.
@MainActor
@Observable
final class AppState {
    enum Phase: Equatable {
        /// Haven't asked the server yet whether the stored token is still
        /// valid. A splash instead of the sign-in screen avoids a flash of it
        /// on every cold launch.
        case bootstrapping
        case loggedOut
        case loggedIn(username: String)
    }

    private(set) var phase: Phase = .bootstrapping
    /// The server this app signs in to: rivolt.dev unless the user chose
    /// another under "Other server". Persisted across launches.
    private(set) var serverURL: URL
    let client: APIClient
    let tokens: TokenStore
    /// Notifications on this iPhone (the Apple token, permission, the switches).
    let notifications: NotificationManager

    private static let serverDefaultsKey = "rivolt.server_url"
    /// The scaffold's old key (a hard-coded self-hosted host); dropped.
    private static let legacyServerDefaultsKey = "rivolt.api_base_url"

    init(tokens: TokenStore = KeychainTokenStore(), defaults: UserDefaults = .standard, notifications: NotificationManager = .shared) {
        defaults.removeObject(forKey: Self.legacyServerDefaultsKey)
        let stored = defaults.string(forKey: Self.serverDefaultsKey).flatMap { try? ServerURL.normalize($0) }
        let server = stored ?? ServerURL.default
        self.serverURL = server
        self.tokens = tokens
        self.notifications = notifications
        self.client = APIClient(baseURL: server, tokens: tokens)
    }

    /// This build's number, compared with the server's `min_ios_build`.
    static var appBuild: Int {
        (Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String).flatMap(Int.init) ?? 0
    }

    /// On cold launch: with a stored token for this server, ask who we are; a
    /// 200 lands on the home screen, anything else on sign-in (an expired
    /// token is the expected case, so no error is shown).
    func bootstrap() async {
        let stored: String? = (try? tokens.token(for: serverURL)) ?? nil
        guard stored != nil else {
            phase = .loggedOut
            return
        }
        do {
            let me = try await client.me()
            phase = .loggedIn(username: me.username)
            await notifications.signedIn(server: client)
        } catch {
            phase = .loggedOut
        }
    }

    /// Checks an address typed under "Other server" and, if it's a Rivolt
    /// server this app can sign in to, switches to it.
    func useServer(_ raw: String) async throws -> AppConfig.Verdict {
        let url = try ServerURL.normalize(raw)
        let probe = APIClient(baseURL: url, tokens: tokens)
        let verdict: AppConfig.Verdict
        do {
            verdict = try await probe.appConfig().verdict(appBuild: Self.appBuild)
        } catch APIError.server(404, _) {
            verdict = .noAppSignIn
        } catch APIError.decoding {
            verdict = .notRivolt
        }
        if verdict == .ready || verdict == .noAppSignIn {
            serverURL = url
            UserDefaults.standard.set(url.absoluteString, forKey: Self.serverDefaultsKey)
            await client.updateBaseURL(url)
        }
        return verdict
    }

    /// Starts sign-in against `serverURL`. The browser-based flow (PKCE and a
    /// one-time code, design §2) lands with the server's app sign-in
    /// endpoints; until then this reports what the server supports.
    func signIn() async throws {
        let config: AppConfig
        do {
            config = try await client.appConfig()
        } catch APIError.server(404, _) {
            throw SignInError.notOnThisServer
        }
        switch config.verdict(appBuild: Self.appBuild) {
        case .notRivolt: throw SignInError.notRivolt
        case .appTooOld: throw SignInError.updateApp
        case .noAppSignIn: throw SignInError.notOnThisServer
        case .ready: throw SignInError.comingSoon
        }
    }

    func logout() async {
        // Best effort on the server; locally the token always goes, so the UI
        // reflects the user's intent even offline.
        // The phone is forgotten for notifications first, while the token still works.
        await notifications.signedOut()
        try? await client.logout()
        try? tokens.deleteToken(for: serverURL)
        phase = .loggedOut
    }
}

enum SignInError: LocalizedError, Equatable {
    case notRivolt, notOnThisServer, updateApp, comingSoon

    var errorDescription: String? {
        switch self {
        case .notRivolt: return "That address isn't a Rivolt server."
        case .notOnThisServer: return "This server doesn't support signing in from the app yet."
        case .updateApp: return "This server needs a newer version of the app."
        case .comingSoon: return "Signing in from the app arrives with the next server update."
        }
    }
}
