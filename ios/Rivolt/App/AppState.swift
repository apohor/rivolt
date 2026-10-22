import Foundation
import Observation
import UIKit
import WidgetKit

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

    /// What the server said about signing in from the app, so the sign-in button
    /// can say it can't work before it is tapped. `unknown` (not asked yet, or no
    /// answer) leaves the button enabled: tapping it checks again and says why.
    enum SignInStatus: Equatable {
        case unknown
        case available
        case unavailable(SignInError)

        var unavailableReason: SignInError? {
            if case .unavailable(let e) = self { return e }
            return nil
        }

        /// From the server's `/api/app/config` answer (or the lack of one).
        static func from(_ result: Result<AppConfig, Error>, appBuild: Int) -> SignInStatus {
            switch result {
            case .success(let config):
                switch config.verdict(appBuild: appBuild) {
                case .ready: return .available
                case .noAppSignIn: return .unavailable(.notOnThisServer)
                case .notRivolt: return .unavailable(.notRivolt)
                case .appTooOld: return .unavailable(.updateApp)
                }
            case .failure(let error):
                switch error {
                case APIError.server(404, _): return .unavailable(.notOnThisServer)
                case APIError.decoding: return .unavailable(.notRivolt)
                default: return .unknown
                }
            }
        }
    }

    private(set) var phase: Phase = .bootstrapping
    private(set) var signInStatus: SignInStatus = .unknown
    /// The server this app signs in to: rivolt.dev unless the user chose
    /// another under "Other server". Persisted across launches.
    private(set) var serverURL: URL
    let client: APIClient
    let tokens: TokenStore
    /// Notifications on this iPhone (the Apple token, permission, the switches).
    let notifications: NotificationManager
    /// The charging Live Activity (started by the app while charging, with the person's switch on).
    let liveActivity: LiveActivityManager

    private static let serverDefaultsKey = "rivolt.server_url"
    /// The scaffold's old key (a hard-coded self-hosted host); dropped.
    private static let legacyServerDefaultsKey = "rivolt.api_base_url"

    init(tokens: TokenStore = KeychainTokenStore(), defaults: UserDefaults = .standard, notifications: NotificationManager? = nil, liveActivity: LiveActivityManager? = nil) {
        defaults.removeObject(forKey: Self.legacyServerDefaultsKey)
        let stored = defaults.string(forKey: Self.serverDefaultsKey).flatMap { try? ServerURL.normalize($0) }
        let server = stored ?? ServerURL.default
        self.serverURL = server
        self.tokens = tokens
        self.notifications = notifications ?? NotificationManager.shared
        self.liveActivity = liveActivity ?? LiveActivityManager.shared
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
        liveActivity.attach(server: client)
        } catch {
            phase = .loggedOut
        }
    }

    #if DEBUG
    /// Screenshot previews keep their fixed sign-in state: nothing is asked of a server.
    private var previewLocked = false
    func setPreviewSignIn(_ status: SignInStatus) {
        signInStatus = status
        phase = .loggedOut
        previewLocked = true
    }
    #endif

    /// Asks the current server whether the app can sign in to it (the sign-in
    /// screen calls this when it appears and after switching server).
    func refreshSignInStatus() async {
        #if DEBUG
        if previewLocked { return }
        #endif
        let server = serverURL
        let result: Result<AppConfig, Error>
        do { result = .success(try await client.appConfig()) } catch { result = .failure(error) }
        guard server == serverURL else { return }
        signInStatus = SignInStatus.from(result, appBuild: Self.appBuild)
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
            // Another server: the widgets must not keep the old one's truck or numbers, nor the map its tiles.
            WidgetStore().clear()
            WidgetCenter.shared.reloadAllTimelines()
            MapNetwork.reset()
            serverURL = url
            UserDefaults.standard.set(url.absoluteString, forKey: Self.serverDefaultsKey)
            await client.updateBaseURL(url)
            signInStatus = .unknown
            await refreshSignInStatus()
        }
        return verdict
    }

    /// Signs in to `serverURL` through the server's web sign-in (design §2):
    /// checks the server, then runs the browser step with `authenticator`. A
    /// cancel in the browser returns quietly; everything else that stops
    /// sign-in throws a `SignInError` for the sign-in screen.
    func signIn(with authenticator: WebAuthenticator) async throws {
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
        case .ready: break
        }
        let server = serverURL
        let flow = SignInFlow(
            server: server,
            client: APIClient(baseURL: server, tokens: tokens),
            tokens: tokens,
            authenticator: authenticator,
            deviceName: UIDevice.current.name
        )
        let username: String
        do {
            username = try await flow.run()
        } catch SignInError.cancelled {
            return
        }
        // The token is stored for the server the sign-in started on; if the
        // setting changed meanwhile, stay signed out of the new one.
        guard serverURL == server else { return }
        phase = .loggedIn(username: username)
        // The widgets were showing "sign in"; they can fetch now.
        WidgetCenter.shared.reloadAllTimelines()
        await notifications.signedIn(server: client)
        liveActivity.attach(server: client)
    }

    func logout() async {
        // Best effort on the server; locally the token always goes, so the UI
        // reflects the user's intent even offline.
        // The phone is forgotten for notifications first, while the token still works.
        await liveActivity.stop()
        liveActivity.attach(server: nil)
        await notifications.signedOut()
        try? await client.logout()
        try? tokens.deleteToken(for: serverURL)
        // The widgets forget the truck and its last numbers with the session; the map forgets its token and cached tiles.
        WidgetStore().clear()
        MapNetwork.reset()
        WidgetCenter.shared.reloadAllTimelines()
        phase = .loggedOut
    }
}

enum SignInError: LocalizedError, Equatable {
    case notRivolt, notOnThisServer, updateApp
    /// Closed in the browser: not an error, nothing is shown.
    case cancelled
    /// Cancel on the server's "Sign in to the Rivolt iPhone app?" page.
    case denied
    /// The one-time code expired or was already used.
    case expired
    /// The sign-in didn't come back as expected.
    case failed
    /// iOS closed the sign-in window before anyone could (see
    /// SystemWebAuthenticator.fastestHumanCancel).
    case couldNotOpen
    /// Any other ASWebAuthenticationSession error, with its code for support.
    case browser(code: Int)

    var errorDescription: String? {
        switch self {
        case .notRivolt: return "That address isn't a Rivolt server."
        case .notOnThisServer: return "This server doesn't support signing in from the app yet."
        case .updateApp: return "This server needs a newer version of the app."
        case .cancelled: return nil
        case .denied: return "Sign-in was cancelled."
        case .expired: return "That sign-in expired. Try again."
        case .failed: return "Sign-in didn't finish. Try again."
        case .couldNotOpen: return "Sign-in couldn't open on this phone. Delete and reinstall Rivolt, then try again."
        case .browser(let code): return "Sign-in couldn't start (error \(code)). Try again."
        }
    }
}
