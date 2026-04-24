import Foundation
import Observation

/// Global app state. Owns the `APIClient` (so every view talks to the
/// same cookie jar + base URL) and the current authenticated user.
///
/// The three legal UI phases are represented by `Phase` rather than a
/// pair of optional booleans — that makes the root view a single
/// `switch` and prevents the "not-yet-loaded vs. logged-out" ambiguity
/// that bites every auth-gated SwiftUI app eventually.
@MainActor
@Observable
final class AppState {
    enum Phase: Equatable {
        /// Haven't asked the server yet whether the stored cookie is
        /// still valid. We show a splash instead of the login form
        /// so the user doesn't see a 100ms flash of LoginView before
        /// HomeView on every cold launch.
        case bootstrapping
        case loggedOut
        case loggedIn(username: String)
    }

    private(set) var phase: Phase = .bootstrapping
    /// Persisted server override. Default comes from Info.plist,
    /// but the login screen lets the user point at a dev instance.
    var serverURLString: String {
        didSet {
            UserDefaults.standard.set(serverURLString, forKey: Self.serverDefaultsKey)
            // `APIClient` is an actor, so mutating its base URL has
            // to hop onto the actor. Fire-and-forget is fine — the
            // next request through `client` will already be queued
            // behind this update in the actor's serial mailbox.
            let latest = serverURLString
            Task { await client.updateBaseURL(latest) }
        }
    }
    let client: APIClient

    private static let serverDefaultsKey = "rivolt.api_base_url"

    init() {
        let fallback = (Bundle.main.object(forInfoDictionaryKey: "RivoltAPIBaseURL") as? String)
            ?? "https://rivolt.apoh.synology.me"
        let stored = UserDefaults.standard.string(forKey: Self.serverDefaultsKey)
        let initial = stored?.isEmpty == false ? stored! : fallback
        self.serverURLString = initial
        self.client = APIClient(baseURLString: initial)
    }

    /// On cold launch, ask the server if our cookie still represents
    /// a valid session. A 200 means we have a username and can skip
    /// the login screen; anything else (401, network error, bad URL)
    /// sends us to login without surfacing an error — the cookie
    /// being expired is the expected case.
    func bootstrap() async {
        do {
            let me = try await client.me()
            phase = .loggedIn(username: me.username)
        } catch {
            phase = .loggedOut
        }
    }

    func login(username: String, password: String) async throws {
        let me = try await client.login(username: username, password: password)
        phase = .loggedIn(username: me.username)
    }

    func logout() async {
        // Best-effort; even if the server round-trip fails, locally
        // tear down the session so the UI reflects the user's
        // intent. The cookie will expire on its own.
        try? await client.logout()
        phase = .loggedOut
    }
}
