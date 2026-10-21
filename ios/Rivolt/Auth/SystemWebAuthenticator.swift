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
        do {
            return try await session.authenticate(
                using: start,
                callback: listen,
                preferredBrowserSession: .shared,
                additionalHeaderFields: [:]
            )
        } catch let error as ASWebAuthenticationSessionError where error.code == .canceledLogin {
            throw SignInError.cancelled
        }
    }
}
