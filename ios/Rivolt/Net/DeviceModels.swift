import Foundation

/// `GET /api/auth/sessions`: the browsers and iPhones signed in as this person, the server's own list. Never a token or an address;
/// the label is what the device called itself when it signed in, shown as stored.
struct SignedInDevice: Decodable, Equatable, Identifiable {
    let id: String
    /// "web" or "app".
    let kind: String
    let deviceLabel: String?
    let createdAt: Date?
    let lastSeenAt: Date?
    /// True for the device making this request.
    let current: Bool

    private enum CodingKeys: String, CodingKey { case id, kind, deviceLabel, createdAt, lastSeenAt, current }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
        deviceLabel = try c.decodeIfPresent(String.self, forKey: .deviceLabel)
        createdAt = try c.decodeIfPresent(Date.self, forKey: .createdAt)
        lastSeenAt = try c.decodeIfPresent(Date.self, forKey: .lastSeenAt)
        current = try c.decodeIfPresent(Bool.self, forKey: .current) ?? false
    }

    init(id: String, kind: String, deviceLabel: String?, createdAt: Date?, lastSeenAt: Date?, current: Bool) {
        self.id = id
        self.kind = kind
        self.deviceLabel = deviceLabel
        self.createdAt = createdAt
        self.lastSeenAt = lastSeenAt
        self.current = current
    }
}

struct SignedInDevices: Decodable, Equatable {
    let sessions: [SignedInDevice]
    /// With a `recent` cut: how many other browsers' sign-ins the list leaves out (0 when it is the whole list).
    var hiddenWeb: Int = 0

    private enum CodingKeys: String, CodingKey { case sessions, hiddenWeb }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        sessions = try c.decode([SignedInDevice].self, forKey: .sessions)
        hiddenWeb = max(0, try c.decodeIfPresent(Int.self, forKey: .hiddenWeb) ?? 0)
    }

    init(sessions: [SignedInDevice], hiddenWeb: Int = 0) {
        self.sessions = sessions
        self.hiddenWeb = hiddenWeb
    }
}
