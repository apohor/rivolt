import Foundation

/// The Rivian connection as `GET /api/settings/rivian/` reports it. Every field is optional: an older server sends fewer, and the
/// app reads only what it needs for the "connection expires" notice. `sessionExpiresAt` and `sessionDaysLeft` come only while the
/// account is signed in and not `needsReauth`.
struct RivianStatus: Decodable, Equatable {
    var enabled: Bool?
    var authenticated: Bool?
    var mfaPending: Bool?
    var needsReauth: Bool?
    var paused: Bool?
    /// RFC 3339: when Rivian ends this sign-in (180 days after it; nothing extends it).
    var sessionExpiresAt: String?
    /// Whole days left, rounded up; 0 once past.
    var sessionDaysLeft: Int?
}
