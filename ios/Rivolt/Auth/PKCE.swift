import CryptoKit
import Foundation
import Security

/// PKCE (RFC 7636) for the app sign-in: the app keeps `verifier`, sends
/// `challenge` (S256) when it opens the server's sign-in page, and proves it
/// started that sign-in by sending `verifier` with the one-time code. A code
/// intercepted on the way back to the app is useless without the verifier.
struct PKCE: Equatable {
    let verifier: String
    let challenge: String

    /// A fresh pair: 32 random bytes, base64url-encoded, gives a 43-character
    /// verifier (the RFC's minimum length, from its unreserved set).
    static func generate() throws -> PKCE {
        var bytes = [UInt8](repeating: 0, count: 32)
        let status = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        guard status == errSecSuccess else { throw PKCEError.randomUnavailable(status) }
        let verifier = Data(bytes).base64URLEncodedString()
        return PKCE(verifier: verifier, challenge: challenge(for: verifier))
    }

    /// S256: base64url(SHA-256(ASCII(verifier))), no padding.
    static func challenge(for verifier: String) -> String {
        let digest = SHA256.hash(data: Data(verifier.utf8))
        return Data(digest).base64URLEncodedString()
    }
}

enum PKCEError: Error, Equatable {
    case randomUnavailable(OSStatus)
}

extension Data {
    /// Base64url (RFC 4648 §5) without padding, as PKCE and JWT use.
    func base64URLEncodedString() -> String {
        base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}
