import Foundation
import MapLibre

/// What MapLibre may do on the network and on disk.
///
/// - The app token goes only on requests to the signed-in server's own origin (the tile archive); a request anywhere
///   else (there should be none: the styles name no other host) leaves without it.
/// - The tile cache is MapLibre's ambient cache, in Caches (Info.plist MLNOfflineStorageDatabasePath, excluded from
///   backup by MapLibre), capped at 50 MB, and emptied on sign-out and on a switch to another server. It holds
///   basemap tiles only, never a route.
@MainActor
enum MapNetwork {
    static let cacheBytes: UInt = 50 * 1024 * 1024

    private static let auth = MapAuthHeader()
    private static var configured = false

    /// Called before a map is shown: the server and its token for this signed-in session.
    static func prepare(server: URL, token: String?) {
        auth.set(origin: MapStyle.origin(server), token: token)
        MLNNetworkConfiguration.sharedManager.delegate = auth
        guard !configured else { return }
        configured = true
        MLNOfflineStorage.shared.setMaximumAmbientCacheSize(cacheBytes, withCompletionHandler: { _ in })
    }

    /// Sign-out or another server: the token is forgotten and the cached tiles go.
    static func reset() {
        auth.set(origin: nil, token: nil)
        MLNOfflineStorage.shared.clearAmbientCache(completionHandler: { _ in })
    }
}

/// The request hook. MapLibre calls it from its own threads, so the state is locked.
final class MapAuthHeader: NSObject, MLNNetworkConfigurationDelegate, @unchecked Sendable {
    private let lock = NSLock()
    private var origin: String?
    private var token: String?

    func set(origin: String?, token: String?) {
        lock.withLock {
            self.origin = origin
            self.token = token
        }
    }

    /// The header for `url`: the bearer token on the server's own origin, nothing elsewhere.
    func header(for url: URL?) -> String? {
        lock.withLock {
            guard let url, let origin, let token, !token.isEmpty, MapStyle.origin(url) == origin else { return nil }
            return "Bearer \(token)"
        }
    }

    @objc(willSendRequest:)
    func willSend(_ request: NSMutableURLRequest) -> NSMutableURLRequest {
        if let h = header(for: request.url) {
            request.setValue(h, forHTTPHeaderField: "Authorization")
        } else {
            request.setValue(nil, forHTTPHeaderField: "Authorization")
        }
        return request
    }
}
