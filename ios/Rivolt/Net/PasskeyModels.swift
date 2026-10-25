import Foundation

/// `GET /api/auth/passkeys`: the passkeys on the account, as the Security tab on the web lists them. Adding and removing stay on the
/// web (the phone's token can't), so this is a read.
struct PasskeyEntry: Decodable, Equatable, Identifiable {
    var id: String
    var label: String
    var createdAt: Date?
    var lastUsedAt: Date?
    /// Can be backed up (iCloud Keychain, a password manager) rather than living on one device.
    var synced: Bool?
}

struct PasskeyList: Decodable, Equatable {
    var passkeys: [PasskeyEntry] = []
    var max: Int?

    init(passkeys: [PasskeyEntry] = [], max: Int? = nil) { self.passkeys = passkeys; self.max = max }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        passkeys = (try? c.decode([PasskeyEntry].self, forKey: .passkeys)) ?? []
        max = try? c.decode(Int.self, forKey: .max)
    }

    private enum CodingKeys: String, CodingKey { case passkeys, max }
}
