import Foundation

/// Where tapping a notification goes inside the app. The server stores each notification's tap target as a same-site path
/// (`push.SafeNotificationURL`: "/" for most kinds, "/money", "/charges/<id>" for a finished charge, "/plan?trip=<id>&tab=pva" for a
/// trip recap). Anything the app doesn't know, or that doesn't parse, is Home: a tap never lands nowhere and never trusts a path to
/// reach beyond these screens.
enum NotificationRoute: Equatable {
    case home
    case money
    /// A finished charge: the Activity tab (the charge's own page is not opened from here yet).
    case charge(String)
    /// A saved trip's recap: the Plan tab with that trip opened, so its Plan vs actual shows.
    case trip(String)

    /// The tab ids AppShell uses.
    var tab: String {
        switch self {
        case .home: return "now"
        case .money: return "money"
        case .charge: return "activity"
        case .trip: return "plan"
        }
    }

    /// The tab to show: this route's, or Now when that tab isn't offered here (no planner on this server).
    func tab(available: [String]) -> String { available.contains(tab) ? tab : "now" }

    /// The route for a stored tap target. Ids are the hex-and-dash ids the server makes, nothing else.
    static func parse(_ target: String?) -> NotificationRoute {
        guard let target, target.hasPrefix("/"), !target.hasPrefix("//"), target.count <= 200 else { return .home }
        let parts = target.split(separator: "?", maxSplits: 1, omittingEmptySubsequences: false)
        let path = String(parts[0])
        let query = parts.count > 1 ? String(parts[1]) : ""
        switch path {
        case "/money":
            return .money
        case "/plan":
            for pair in query.split(separator: "&") {
                let kv = pair.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
                if kv.count == 2, kv[0] == "trip", isID(String(kv[1])) { return .trip(String(kv[1])) }
            }
            return .home
        default:
            if path.hasPrefix("/charges/") {
                let id = String(path.dropFirst("/charges/".count))
                if isID(id) { return .charge(id) }
            }
            return .home
        }
    }

    private static func isID(_ s: String) -> Bool {
        !s.isEmpty && s.count <= 64 && s.allSatisfy { $0.isHexDigit || $0 == "-" }
    }
}
