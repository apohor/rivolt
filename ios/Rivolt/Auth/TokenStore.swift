import Foundation
import Security

/// Where the app keeps its session token: one per server, so a token issued
/// by one server is never sent to another (DA's condition on the design).
protocol TokenStore: Sendable {
    func token(for server: URL) throws -> String?
    func setToken(_ token: String, for server: URL) throws
    func deleteToken(for server: URL) throws
}

/// The Keychain-backed store. Generic-password items, one per server origin,
/// readable after the first unlock and never migrated to another device
/// (widgets and background refresh need it while the phone is locked).
struct KeychainTokenStore: TokenStore {
    /// Separate services keep tests from touching a real session.
    let service: String

    init(service: String = "dev.rivolt.session") {
        self.service = service
    }

    func token(for server: URL) throws -> String? {
        var query = baseQuery(for: server)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        switch status {
        case errSecSuccess:
            guard let data = item as? Data, let token = String(data: data, encoding: .utf8) else {
                throw TokenStoreError.corrupt
            }
            return token
        case errSecItemNotFound:
            return nil
        default:
            throw TokenStoreError.keychain(status)
        }
    }

    func setToken(_ token: String, for server: URL) throws {
        let data = Data(token.utf8)
        let update: [String: Any] = [kSecValueData as String: data]
        let status = SecItemUpdate(baseQuery(for: server) as CFDictionary, update as CFDictionary)
        switch status {
        case errSecSuccess:
            return
        case errSecItemNotFound:
            var add = baseQuery(for: server)
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            let added = SecItemAdd(add as CFDictionary, nil)
            guard added == errSecSuccess else { throw TokenStoreError.keychain(added) }
        default:
            throw TokenStoreError.keychain(status)
        }
    }

    func deleteToken(for server: URL) throws {
        let status = SecItemDelete(baseQuery(for: server) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw TokenStoreError.keychain(status)
        }
    }

    private func baseQuery(for server: URL) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: ServerURL.origin(of: server),
        ]
    }
}

enum TokenStoreError: Error, Equatable {
    case keychain(OSStatus)
    case corrupt
}
