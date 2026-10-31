import Foundation

/// A service notice from `GET /api/notices` (the Pro launch notice, the founding-member end). The words are the server's, the same as the
/// email's and the web's. `dismissed` hides the banner on Now; the Settings row shows the notice until the server stops listing it.
struct ServiceNotice: Decodable, Equatable, Identifiable {
    struct Link: Decodable, Equatable {
        let label: String
        let href: String
    }

    let kind: String
    let title: String
    let body: String
    let links: [Link]
    let dismissed: Bool
    var id: String { kind }

    init(kind: String, title: String, body: String, links: [Link], dismissed: Bool) {
        self.kind = kind; self.title = title; self.body = body; self.links = links; self.dismissed = dismissed
    }
}

enum NoticeLogic {
    /// The first notice the person has not dismissed.
    static func banner(_ list: [ServiceNotice]) -> ServiceNotice? { list.first { !$0.dismissed } }

    /// A link from the server is a path of this server or an https address; anything else (javascript:, http:, `//host`) is not a link.
    static func url(_ href: String, server: URL) -> URL? {
        if href.hasPrefix("/"), !href.hasPrefix("//") {
            return URL(string: href, relativeTo: server)?.absoluteURL
        }
        guard let u = URL(string: href), u.scheme == "https", u.host != nil else { return nil }
        return u
    }
}
