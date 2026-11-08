#if DEBUG && QA_SIGNIN
import Foundation
import os

/// A signed-in session for the simulator, for QA runs against the preview server (never in Release or TestFlight: the QA_SIGNIN flag is set in
/// the Debug configuration only, and CI checks the Release binary for QA_SIGNIN_HOOK_MARKER). The launch environment carries a session token QA
/// minted through the app's own sign-in (RIVOLT_QA_TOKEN) and the server it belongs to (RIVOLT_QA_SERVER).
/// Only the preview origin is accepted; any other server, the production one included, is ignored. The token is never logged.
enum QASignIn {
    /// The one server the hook will sign in to.
    static let previewOrigin = "https://preview.rivolt.dev"
    static let marker = "QA_SIGNIN_HOOK_MARKER"

    enum Outcome: Equatable { case notAsked, refused, signedIn }

    /// Stores the token for the preview server and points the app at it. Call before the app reads its server.
    @discardableResult
    static func apply(environment: [String: String] = ProcessInfo.processInfo.environment, tokens: TokenStore, defaults: UserDefaults, serverKey: String) -> Outcome {
        guard let raw = environment["RIVOLT_QA_SERVER"], let token = environment["RIVOLT_QA_TOKEN"], !token.isEmpty else { return .notAsked }
        guard let url = try? ServerURL.normalize(raw), url.absoluteString == previewOrigin else {
            Logger(subsystem: "dev.rivolt.app", category: "qa").error("\(marker): not the preview server")
            return .refused
        }
        guard (try? tokens.setToken(token, for: url)) != nil else { return .refused }
        defaults.set(url.absoluteString, forKey: serverKey)
        return .signedIn
    }
}
#endif
