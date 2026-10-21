import Foundation

/// `GET /api/app/config`: how the app checks that an address is a Rivolt
/// server it can sign in to. A UX check, not a security boundary (tokens are
/// bound to the server that issued them).
struct AppConfig: Decodable, Equatable {
    let server: String
    let apiVersion: Int
    let appLogin: Bool
    let minIosBuild: Int?
    /// Which push transports the server runs. Absent on a server that predates push.
    var push: Push? = nil

    struct Push: Decodable, Equatable {
        let apns: Bool
        /// True only when the server sends Live Activity updates. Absent on a server that predates them.
        var live: Bool? = nil
    }

    /// Whether this server sends notifications to the app (APNs is configured).
    var apnsAvailable: Bool { push?.apns == true }

    /// Whether this server sends charging Live Activity updates (needs APNs too).
    var liveAvailable: Bool { apnsAvailable && push?.live == true }

    /// What the sign-in screen does with a server's answer.
    enum Verdict: Equatable {
        case ready
        case notRivolt
        case noAppSignIn
        case appTooOld(minimumBuild: Int)
    }

    func verdict(appBuild: Int) -> Verdict {
        guard server == "rivolt" else { return .notRivolt }
        if let min = minIosBuild, appBuild < min { return .appTooOld(minimumBuild: min) }
        return appLogin ? .ready : .noAppSignIn
    }
}
