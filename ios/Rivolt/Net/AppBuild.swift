import Foundation

/// This build's number (CFBundleVersion), sent on every request to the server as `X-Rivolt-App-Build`. Only the number: no device or person identifier.
enum AppBuild {
    static let header = "X-Rivolt-App-Build"

    static let number: Int = (Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String).flatMap(Int.init) ?? 0
}
