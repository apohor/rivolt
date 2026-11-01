import Foundation
import Security

/// A random id for this install of the app, made once and kept in the Keychain (readable after the first unlock, never moved to another
/// device or restored from a backup to one). It is sent with the Apple device token (`install_id`) so the server can tell this phone from
/// someone else presenting the same token: a token already registered to another account under another install is refused (409). It
/// carries nothing about the person or the phone.
enum InstallID {
    /// Far under the server's 128-character limit.
    static let length = 64

    static func make() -> String {
        var bytes = [UInt8](repeating: 0, count: length / 2)
        if SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) != errSecSuccess {
            bytes = (0..<bytes.count).map { _ in UInt8.random(in: 0...255) }
        }
        return bytes.map { String(format: "%02x", $0) }.joined()
    }

    /// This install's id, made on first use. Nil when the Keychain won't hold one (the registration then goes without it, as before).
    static func current(service: String = "dev.rivolt.install") -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: "apns",
        ]
        var read = query
        read[kSecReturnData as String] = true
        read[kSecMatchLimit as String] = kSecMatchLimitOne
        var item: CFTypeRef?
        if SecItemCopyMatching(read as CFDictionary, &item) == errSecSuccess, let data = item as? Data, let id = String(data: data, encoding: .utf8), !id.isEmpty {
            return id
        }
        let id = make()
        var add = query
        add[kSecValueData as String] = Data(id.utf8)
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(add as CFDictionary, nil)
        if status == errSecDuplicateItem { return current(service: service) }
        return status == errSecSuccess ? id : nil
    }

    /// Removes this install's id (tests).
    static func reset(service: String = "dev.rivolt.install") {
        SecItemDelete([kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: "apns"] as CFDictionary)
    }
}
