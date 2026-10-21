import Foundation

/// What the Notification Service Extension needs, kept free of UIKit so the
/// app's unit tests cover it. An APNs payload carries only `{kind, id}` and a
/// generic alert (Apple's servers never see the text); the extension fetches the
/// real title and body from Rivolt over TLS with the signed-in token.
enum NotificationFetch {
    struct Text: Decodable, Equatable {
        let title: String
        let body: String
    }

    /// The notification id from the payload, only if it looks like the random id
    /// the server makes (hex and dashes): it goes into a URL path.
    static func id(from userInfo: [AnyHashable: Any]) -> String? {
        guard let raw = userInfo["id"] as? String else { return nil }
        let ok = !raw.isEmpty && raw.count <= 64 && raw.allSatisfy { $0.isHexDigit || $0 == "-" }
        return ok ? raw : nil
    }

    /// `GET /api/push/notifications/{id}` with the bearer token. The token is the
    /// one this server issued; it is never sent anywhere else.
    static func request(id: String, server: URL, token: String) -> URLRequest? {
        guard id.allSatisfy({ $0.isHexDigit || $0 == "-" }),
              let url = URL(string: "/api/push/notifications/\(id)", relativeTo: server)?.absoluteURL else { return nil }
        var req = URLRequest(url: url)
        req.httpMethod = "GET"
        req.timeoutInterval = 10
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        return req
    }

    /// The title and body, or nil when the server's answer isn't usable (then the
    /// generic alert stays as it is).
    static func decode(_ data: Data) -> Text? {
        guard let t = try? JSONDecoder().decode(Text.self, from: data) else { return nil }
        let title = t.title.trimmingCharacters(in: .whitespacesAndNewlines)
        let body = t.body.trimmingCharacters(in: .whitespacesAndNewlines)
        return (title.isEmpty && body.isEmpty) ? nil : Text(title: title, body: body)
    }

    /// Fetches the text. Any failure, a 404 (not this person's, or older than a
    /// day) included, returns nil.
    static func text(id: String, tokens: TokenStore = KeychainTokenStore(), server: URL = ServerURL.default, session: URLSession = .shared) async -> Text? {
        guard let token = (try? tokens.token(for: server)) ?? nil,
              let req = request(id: id, server: server, token: token) else { return nil }
        guard let (data, resp) = try? await session.data(for: req),
              let http = resp as? HTTPURLResponse, http.statusCode == 200 else { return nil }
        return decode(data)
    }
}
