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
        /// The demo truck: the sample account, read only, held in memory (no sign-in, no token). See `DemoMode`.
        case demo
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

    private(set) var phase: Phase = .bootstrapping {
        didSet { if case .loggedIn = phase {} else { termsRequired = false } }
    }
    /// A line the sign-in screen shows once, after the session ended in a way the person should be told about (the account deleted on the web).
    private(set) var signInNotice: String?
    /// Set when the person was sent to the web to delete the account: the next time the app is in front, it checks whether they did.
    private var awaitingDeletion = false
    /// True while the server says the current Terms must be accepted before it answers data routes (they are accepted on the web).
    private(set) var termsRequired = false
    /// The account the demo was opened from, when it was opened from one (see `enterDemoFromAccount`).
    private var resumeAfterDemo: String?
    /// What the Connection details screen shows: the last two reads' outcomes and the newest frame, in memory only.
    let connection = ConnectionDiagnostics()
    private(set) var signInStatus: SignInStatus = .unknown
    /// The server this app signs in to: rivolt.dev unless the user chose
    /// another under "Other server". Persisted across launches.
    private(set) var serverURL: URL
    /// The client the screens read through: the signed-in one, or the demo's while the demo truck is open.
    private(set) var client: APIClient
    private let signedInClient: APIClient
    let tokens: TokenStore
    /// Notifications on this iPhone (the Apple token, permission, the switches).
    let notifications: NotificationManager
    /// The charging Live Activity (started by the app while charging, with the person's switch on).
    let liveActivity: LiveActivityManager
    /// The Activity map's drives and charges (coordinates, in memory only): dropped on sign-out and on another server.
    let activityMap = ActivityMapModel()
    /// Activity → Efficiency's figures (aggregates, in memory only): dropped with the Activity map.
    let efficiency = EfficiencyModel()

    private static let serverDefaultsKey = "rivolt.server_url"
    /// The time scope Now, Activity and Truck share (the web's picker: Day, Week, Month or Custom, with This year and All time inside
    /// Custom), remembered on this phone. Money keeps its own month.
    private(set) var period: Period
    private let periodDefaults: UserDefaults
    private static let periodDefaultsKey = "rivolt.period"

    func setPeriod(_ p: Period) {
        period = p
        if let data = try? JSONEncoder().encode(p.stored(now: .now, calendar: .current)) {
            periodDefaults.set(data, forKey: Self.periodDefaultsKey)
        }
    }

    /// The scaffold's old key (a hard-coded self-hosted host); dropped.
    private static let legacyServerDefaultsKey = "rivolt.api_base_url"

    init(tokens: TokenStore = KeychainTokenStore(), defaults: UserDefaults = .standard, notifications: NotificationManager? = nil, liveActivity: LiveActivityManager? = nil,
         session: URLSession? = nil) {
        defaults.removeObject(forKey: Self.legacyServerDefaultsKey)
        let stored = defaults.string(forKey: Self.serverDefaultsKey).flatMap { try? ServerURL.normalize($0) }
        let server = stored ?? ServerURL.default
        self.serverURL = server
        self.periodDefaults = defaults
        self.period = defaults.data(forKey: Self.periodDefaultsKey).flatMap { try? JSONDecoder().decode(Period.Stored.self, from: $0) }
            .map { Period($0, now: .now, calendar: .current) } ?? Period(containing: .now, calendar: .current)
        self.tokens = tokens
        self.notifications = notifications ?? NotificationManager.shared
        self.liveActivity = liveActivity ?? LiveActivityManager.shared
        let signedIn = APIClient(baseURL: server, tokens: tokens, session: session)
        self.client = signedIn
        self.signedInClient = signedIn
        Task { await signedIn.setTermsRequiredHandler { [weak self] in Task { @MainActor in self?.termsRequired = true } } }
    }

    var isDemo: Bool { phase == .demo }

    /// The server the maps draw their tiles from, and the token they send: the signed-in server and its token, or, in the demo, the
    /// demo server's open tile archive with no token (the signed-in server would refuse the demo's anonymous tile requests).
    var mapServer: URL { isDemo ? DemoMode.baseURL : serverURL }
    var mapToken: String? { isDemo ? nil : (try? tokens.token(for: serverURL)) ?? nil }

    /// Opens the demo truck from the sign-in screen. Nothing is stored and nothing is registered: no token, no notification permission,
    /// no widget or Live Activity.
    func enterDemo() {
        guard phase == .loggedOut else { return }
        resumeAfterDemo = nil
        client = APIClient.demo()
        phase = .demo
    }

    /// The sample truck for someone signed in who has no truck yet. The account stays signed in underneath (nothing of it is used
    /// while the demo is open); leaving the demo returns to it.
    func enterDemoFromAccount() {
        guard case .loggedIn(let username) = phase else { return }
        resumeAfterDemo = username
        client = APIClient.demo()
        phase = .demo
    }

    /// True while the demo was opened from a signed-in account: its way out leads back there, not to the sign-in screen.
    var demoResumesAccount: Bool { resumeAfterDemo != nil }

    /// Leaves the demo, forgetting everything it held in memory: back to the account it was opened from, or to the sign-in screen.
    func leaveDemo() {
        guard phase == .demo else { return }
        client = signedInClient
        MapNetwork.reset()
        activityMap.clear()
        efficiency.clear()
        if let username = resumeAfterDemo {
            resumeAfterDemo = nil
            phase = .loggedIn(username: username)
        } else {
            phase = .loggedOut
        }
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
            await refreshUnits(known: me.units)
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
            DisplayUnits.reset()
            WidgetCenter.shared.reloadAllTimelines()
            MapNetwork.reset()
            activityMap.clear()
            efficiency.clear()
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
        await refreshUnits()
        // The widgets were showing "sign in"; they can fetch now.
        WidgetCenter.shared.reloadAllTimelines()
        await notifications.signedIn(server: client)
        liveActivity.attach(server: client)
    }

    /// The person is being sent to the web page that deletes the account (Apple's 5.1.1(v) asks for a way to delete it from the app).
    /// The app never deletes anything itself: its token can't. Coming back to the app, `recheckAfterDeletion` looks.
    func accountDeletionStarted() { awaitingDeletion = true }

    /// After the web deletion page was opened, and the app is in front again: if the server no longer knows this session (deleting an
    /// account ends every session), the app signs out here, forgetting what it held, and the sign-in screen says why. Any other answer
    /// (still signed in, or the server unreachable) leaves things as they were and asks again next time.
    func recheckAfterDeletion() async {
        guard awaitingDeletion, case .loggedIn = phase else { return }
        do {
            _ = try await client.me()
        } catch APIError.server(let status, _) where status == 401 {
            awaitingDeletion = false
            await forgetSession(notice: "Signed out. If you deleted your account, it is gone.")
        } catch {
            return
        }
    }

    /// Ends this phone's session without asking the server (it has already ended it): the token, the notification registration
    /// and the widgets' and map's data all go, and the sign-in screen is shown with `notice`.
    private func forgetSession(notice: String) async {
        await liveActivity.stop()
        liveActivity.attach(server: nil)
        try? tokens.deleteToken(for: serverURL)
        WidgetStore().clear()
        DisplayUnits.reset()
        MapNetwork.reset()
        activityMap.clear()
        efficiency.clear()
        WidgetCenter.shared.reloadAllTimelines()
        signInNotice = notice
        phase = .loggedOut
    }

    func clearSignInNotice() { signInNotice = nil }

    /// Bumped when the units shown change, so the screens read them again.
    private(set) var unitsRevision = 0

    /// The account's units become the phone's. An account that has none yet is seeded once from the phone's region (a server that
    /// has no such setting leaves the phone's region in charge).
    func refreshUnits(known: AccountUnits? = nil) async {
        guard case .loggedIn = phase else { return }
        var units = known
        if units == nil { units = try? await client.accountUnits() }
        guard var u = units else { return }
        if u.isUnset, let seeded = try? await client.setUnits(UnitsPatch(distanceUnit: DisplayUnits.regionIsMetric ? "km" : "mi", temperatureUnit: nil, seed: true)) { u = seeded }
        applyUnits(u, deferRevision: false)
    }

    /// A pick on the Units screen: saved to the account, then shown. False when the server refused or could not be reached.
    func setUnits(distance: String? = nil, temperature: String? = nil) async -> Bool {
        guard case .loggedIn = phase, let u = try? await client.setUnits(UnitsPatch(distanceUnit: distance, temperatureUnit: temperature, seed: nil)) else { return false }
        applyUnits(u, deferRevision: true)
        return true
    }

    /// The units were picked on the Units screen, which stays open while the screens behind it are rebuilt when it closes.
    private var unitsChangePending = false

    func unitsScreenClosed() {
        guard unitsChangePending else { return }
        unitsChangePending = false
        unitsRevision += 1
    }

    private func applyUnits(_ u: AccountUnits, deferRevision: Bool) {
        let before = (DisplayUnits.distanceMetric, DisplayUnits.celsius)
        DisplayUnits.apply(distance: u.distanceUnit, temperature: u.temperatureForDisplay)
        guard before != (DisplayUnits.distanceMetric, DisplayUnits.celsius) else { return }
        if deferRevision { unitsChangePending = true } else { unitsRevision += 1 }
    }

    /// Asks the server whether the Terms still have to be accepted. An unreachable server leaves the answer as it was.
    func recheckTerms() async {
        guard case .loggedIn = phase else { return }
        if let me = try? await client.me() { termsRequired = me.terms?.isRequired ?? false }
    }

    func logout() async {
        // The demo has no session to end: "Sign in" there just leaves it.
        if isDemo { leaveDemo(); return }
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
        DisplayUnits.reset()
        MapNetwork.reset()
        activityMap.clear()
        efficiency.clear()
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
