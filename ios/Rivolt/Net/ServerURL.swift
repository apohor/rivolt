import Foundation

/// The server address the user types under "Other server". Only an https
/// origin is accepted (scheme + host + optional port, no path, query or
/// credentials), so the app can't be pointed at a page inside some other site
/// and every token stays keyed to one origin.
enum ServerURL {
    /// rivolt.dev, the hosted service; self-hosters change it.
    static let `default` = URL(string: "https://rivolt.dev")!

    /// Normalizes user input: trims, adds https:// when no scheme was typed,
    /// lowercases the host, drops a trailing slash. Throws for anything that
    /// isn't a plain https origin.
    static func normalize(_ raw: String) throws -> URL {
        var text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { throw ServerURLError.empty }
        if !text.contains("://") { text = "https://" + text }
        while text.hasSuffix("/") { text.removeLast() }
        guard var parts = URLComponents(string: text) else { throw ServerURLError.invalid }
        guard parts.scheme?.lowercased() == "https" else { throw ServerURLError.notHTTPS }
        guard let host = parts.host, !host.isEmpty else { throw ServerURLError.invalid }
        guard parts.user == nil, parts.password == nil,
              parts.path.isEmpty, parts.query == nil, parts.fragment == nil else {
            throw ServerURLError.notAnOrigin
        }
        parts.scheme = "https"
        parts.host = host.lowercased()
        guard let url = parts.url else { throw ServerURLError.invalid }
        return url
    }

    /// "https://host[:port]": the key a token is stored under.
    static func origin(of url: URL) -> String {
        var s = "https://" + (url.host ?? "").lowercased()
        if let port = url.port { s += ":\(port)" }
        return s
    }
}

enum ServerURLError: LocalizedError, Equatable {
    case empty, invalid, notHTTPS, notAnOrigin

    var errorDescription: String? {
        switch self {
        case .empty: return "Enter the server address."
        case .invalid: return "That isn't a valid server address."
        case .notHTTPS: return "The server must use https://."
        case .notAnOrigin: return "Enter only the server address, like https://rivolt.example.com."
        }
    }
}
