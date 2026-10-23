import SwiftUI

/// "Release notes ↗" for a software version. The link is fetched when it is tapped (Rivian's own is a one-hour signed
/// URL, so the server asks for a fresh one) and handed straight to Safari. A failure says why in a fixed phrase, never
/// in the error's own text.
struct ReleaseNotesLink: View {
    let vehicleID: String
    let which: String
    let fetch: (_ vehicleID: String, _ which: String) async throws -> URL

    @Environment(\.openURL) private var openURL
    @State private var loading = false
    @State private var failure: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Button {
                Task { await open() }
            } label: {
                Text(loading ? "Opening…" : "Release notes ↗").themeText(.subline, color: Theme.accent)
            }
            .buttonStyle(.plain)
            .disabled(loading)
            if let failure {
                Text(failure).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func open() async {
        loading = true
        failure = nil
        defer { loading = false }
        do {
            openURL(try await fetch(vehicleID, which))
        } catch {
            failure = Self.message(for: error)
        }
    }

    /// What to tell the user, by how it failed.
    static func message(for error: Error) -> String {
        switch error {
        case APIError.server(429, _): return "Try again in a few minutes."
        case APIError.server(401, _): return "Reconnect Rivian to open release notes."
        case APIError.server(404, _): return "No release notes for this version."
        default: return "Release notes unavailable right now."
        }
    }
}
