import AuthenticationServices
import SwiftUI

/// The real browser step: SwiftUI's `webAuthenticationSession` action
/// (ASWebAuthenticationSession). The shared browser session lets an existing
/// rivolt.dev web login be reused; the server's consent page asks either way.
struct SystemWebAuthenticator: WebAuthenticator {
    let session: WebAuthenticationSession

    func authenticate(start: URL, callback: SignInCallback) async throws -> URL {
        let listen: ASWebAuthenticationSession.Callback
        switch callback {
        case let .https(host, path): listen = .https(host: host, path: path)
        case let .customScheme(scheme): listen = .customScheme(scheme)
        }
        let started = Date()
        do {
            return try await session.authenticate(
                using: start,
                callback: listen,
                preferredBrowserSession: .shared,
                additionalHeaderFields: [:]
            )
        } catch {
            throw Self.signInError(for: error, after: Date().timeIntervalSince(started))
        }
    }

    /// A person needs more than a second to read the system's "Rivolt wants
    /// to use rivolt.dev to sign in" prompt and tap Cancel. A cancel sooner
    /// than that is iOS refusing to open the session, typically because this
    /// phone hasn't verified the app's rivolt.dev association yet (installed
    /// while the server didn't publish it): say so instead of nothing.
    static let fastestHumanCancel: TimeInterval = 1.0

    static func signInError(for error: Error, after elapsed: TimeInterval) -> Error {
        guard let session = error as? ASWebAuthenticationSessionError else { return error }
        if session.code == .canceledLogin {
            return elapsed < fastestHumanCancel ? SignInError.couldNotOpen : SignInError.cancelled
        }
        return SignInError.browser(code: session.code.rawValue)
    }
}
