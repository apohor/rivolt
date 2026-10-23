import Foundation

/// What a failed read says on screen: plain words for the kind of failure, never an error's own description, which can
/// carry the request's URL, the server host or the underlying text. (`NowReads.describe` is the same idea for the log.)
enum UserFacingError {
    static func words(_ error: Error) -> String {
        switch error {
        case APIError.server(let status, _): return server(status)
        case APIError.decoding, is DecodingError: return "The server's answer wasn't what the app expected. Update the app, or try again in a moment."
        case APIError.badURL: return "The server address isn't valid."
        case APIError.transport: return "No answer from the server. Check your connection and try again."
        case let url as URLError:
            switch url.code {
            case .timedOut: return "The server took too long to answer. Try again in a moment."
            case .notConnectedToInternet, .networkConnectionLost, .dataNotAllowed, .internationalRoamingOff: return "You're offline. Check your connection and try again."
            case .cannotConnectToHost, .cannotFindHost, .dnsLookupFailed, .secureConnectionFailed, .serverCertificateUntrusted: return "Can't reach your Rivolt server. Try again in a moment."
            default: return "Couldn't reach the server. Try again in a moment."
            }
        default: return "Something went wrong. Try again in a moment."
        }
    }

    private static func server(_ status: Int) -> String {
        switch status {
        case 401: return "Your sign-in has ended. Sign in again."
        case 403: return "This server doesn't allow that from the app."
        case 404: return "The server doesn't have that."
        case 429: return "Too many requests. Wait a minute, then try again."
        case 500...599: return "The server had a problem. Try again in a moment."
        default: return "The server couldn't do that (error \(status))."
        }
    }
}
