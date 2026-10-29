import Foundation

/// What the app (not the notification extension, which compiles NotificationFetch.swift on its own) does when a notification is tapped:
/// read the stored tap target from the same answer the extension fetches, and turn it into a screen.
extension NotificationFetch {
    /// The stored tap target (`url`) of a notification answer, or nil when it has none.
    static func tapURL(_ data: Data) -> String? {
        struct Tap: Decodable { let url: String? }
        return (try? JSONDecoder().decode(Tap.self, from: data))?.url
    }

    /// Where a tapped notification goes: its stored target. Any failure (offline, expired, not this person's) is Home.
    static func route(id: String, tokens: TokenStore = KeychainTokenStore(), server: URL = ServerURL.default, session: URLSession = .shared) async -> NotificationRoute {
        guard let token = (try? tokens.token(for: server)) ?? nil,
              let req = request(id: id, server: server, token: token),
              let (data, resp) = try? await session.data(for: req),
              let http = resp as? HTTPURLResponse, http.statusCode == 200 else { return .home }
        return NotificationRoute.parse(tapURL(data))
    }
}
